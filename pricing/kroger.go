package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Endpoints are package vars (not consts) so tests can point them at an
// httptest server.
var (
	krogerTokenURL   = "https://api.kroger.com/v1/connect/oauth2/token"
	krogerProductURL = "https://api.kroger.com/v1/products"
)

const (
	// krogerHTTPTimeout bounds one full exchange including any in-transport
	// retry sleeps; the caller's context is the real deadline.
	krogerHTTPTimeout = 60 * time.Second
	// krogerTokenSkew refreshes a token this long before it actually expires.
	krogerTokenSkew = 30 * time.Second
	// krogerParkDefault is the cooldown applied to a credential after a 429
	// with no Retry-After, or an auth failure.
	krogerParkDefault = 60 * time.Second
	// krogerRetryAfterCap clamps a server-supplied Retry-After so one hostile
	// header can't stall a request past krogerHTTPTimeout.
	krogerRetryAfterCap    = 30 * time.Second
	krogerDailyCapFallback = 9500
)

// krogerMaxRetries is the in-transport retry budget for 429/5xx. A var so
// tests can lower it.
var krogerMaxRetries = 3

// ─────────────────────────── rate limiter ───────────────────────────

// krogerLimiter is a rolling one-minute-window limiter, ported from the
// GuardianIT action1-api client. Wait blocks until a slot is free.
type krogerLimiter struct {
	mu    sync.Mutex
	times []time.Time
	limit int
}

func newKrogerLimiter(perMinute int) *krogerLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	return &krogerLimiter{limit: perMinute}
}

func (l *krogerLimiter) Wait() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	window := now.Add(-time.Minute)

	valid := l.times[:0]
	for _, t := range l.times {
		if t.After(window) {
			valid = append(valid, t)
		}
	}
	l.times = valid

	if len(l.times) >= l.limit {
		time.Sleep(l.times[0].Add(time.Minute).Sub(now))
	}
	l.times = append(l.times, time.Now())
}

// ─────────────────────────── retry transport ───────────────────────────

// krogerRetryTransport retries 429/500/502/503 up to krogerMaxRetries times,
// honouring Retry-After and otherwise backing off exponentially with jitter.
// Ported from the GuardianIT action1-api client. Retries re-enter the shared
// limiter so a 429 storm can't fan out uncounted requests.
type krogerRetryTransport struct {
	base       http.RoundTripper
	maxRetries int
	limiter    *krogerLimiter
}

func (t *krogerRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 {
			cloned := req.Clone(req.Context())
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				cloned.Body = body
			}
			r = cloned
			if t.limiter != nil {
				t.limiter.Wait()
			}
		}

		resp, err := t.base.RoundTrip(r)
		if err != nil {
			return nil, err
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusInternalServerError ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable
		if !retryable || attempt >= t.maxRetries {
			return resp, nil
		}

		retryAfter := ""
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter = resp.Header.Get("Retry-After")
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(krogerRetryDelay(attempt, retryAfter)):
		}
	}
}

// krogerRetryDelay prefers a numeric Retry-After (seconds, clamped); otherwise
// exponential backoff starting at 1s, doubling, capped at 30s, plus up to ~10%
// jitter.
func krogerRetryDelay(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > krogerRetryAfterCap {
				d = krogerRetryAfterCap
			}
			return d
		}
	}
	base := time.Duration(1<<uint(attempt)) * time.Second
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return base + time.Duration(rand.Int63n(int64(base)/10+1))
}

// ─────────────────────────── credential pool ───────────────────────────

// krogerCred is one client_id/secret pair with its own cached token and its
// own daily-call budget. Kroger's 10k/day Products cap is per registered app,
// so each credential tracks its own usage.
type krogerCred struct {
	id, secret string

	mu       sync.Mutex
	token    string
	tokenExp time.Time

	parkedUntil time.Time // cooldown after 429 / auth failure
	dayCount    int
	dayStart    time.Time
}

func (c *krogerCred) available(now time.Time, dailyCap int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dayStart.IsZero() && now.Sub(c.dayStart) >= 24*time.Hour {
		c.dayStart, c.dayCount = time.Time{}, 0
	}
	return now.After(c.parkedUntil) && c.dayCount < dailyCap
}

func (c *krogerCred) recordCall() {
	c.mu.Lock()
	if c.dayStart.IsZero() {
		c.dayStart = time.Now()
	}
	c.dayCount++
	c.mu.Unlock()
}

func (c *krogerCred) park(d time.Duration) {
	c.mu.Lock()
	c.parkedUntil = time.Now().Add(d)
	c.token, c.tokenExp = "", time.Time{} // force a fresh token after cooldown
	c.mu.Unlock()
}

func (c *krogerCred) clearToken() {
	c.mu.Lock()
	c.token, c.tokenExp = "", time.Time{}
	c.mu.Unlock()
}

// ─────────────────────────── provider ───────────────────────────

// KrogerProvider prices items against the Kroger Products API (§6.2
// OfficialAPIProvider). It rate-limits every call, retries transient failures
// with backoff, and rotates across a pool of client credentials when one is
// throttled or spends its daily quota. It fails soft: any unrecovered error
// returns (nil, nil) so the resolution chain moves on.
type KrogerProvider struct {
	creds      []*krogerCred
	locationID string
	dailyCap   int
	client     *http.Client
	limiter    *krogerLimiter

	mu   sync.Mutex // guards the round-robin cursor
	next int
}

// NewKrogerProvider builds the adapter from a pool of {clientID, clientSecret}
// pairs. It returns nil when the pool is empty (no usable credentials), which
// the chain builder treats as "skip this provider". maxRPM and dailyCap take
// sane defaults when non-positive.
func NewKrogerProvider(creds [][2]string, locationID string, maxRPM, dailyCap int) *KrogerProvider {
	pool := make([]*krogerCred, 0, len(creds))
	for _, c := range creds {
		if c[0] == "" || c[1] == "" {
			continue
		}
		pool = append(pool, &krogerCred{id: c[0], secret: c[1]})
	}
	if len(pool) == 0 {
		return nil
	}
	if dailyCap <= 0 {
		dailyCap = krogerDailyCapFallback
	}
	lim := newKrogerLimiter(maxRPM)
	return &KrogerProvider{
		creds:      pool,
		locationID: locationID,
		dailyCap:   dailyCap,
		limiter:    lim,
		client: &http.Client{
			Timeout: krogerHTTPTimeout,
			Transport: &krogerRetryTransport{
				base:       http.DefaultTransport,
				maxRetries: krogerMaxRetries,
				limiter:    lim,
			},
		},
	}
}

func (k *KrogerProvider) Name() string { return "kroger" }

// acquire returns the next usable credential in round-robin order, or nil when
// every credential is parked or over its daily cap.
func (k *KrogerProvider) acquire() *krogerCred {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	for i := 0; i < len(k.creds); i++ {
		c := k.creds[(k.next+i)%len(k.creds)]
		if c.available(now, k.dailyCap) {
			k.next = (k.next + i + 1) % len(k.creds)
			return c
		}
	}
	return nil
}

func (k *KrogerProvider) Lookup(ctx context.Context, term string, _ int64, _ string) (*PriceResult, error) {
	for i := 0; i < len(k.creds); i++ {
		c := k.acquire()
		if c == nil {
			log.Printf("kroger: all %d credential(s) parked or over daily cap; skipping %q", len(k.creds), term)
			return nil, nil
		}

		res, err := k.lookupWith(ctx, c, term)
		if err == nil {
			return res, nil
		}

		var he *krogerHTTPError
		switch {
		case errors.As(err, &he) && he.status == http.StatusTooManyRequests:
			c.park(he.cooldown())
			log.Printf("kroger: credential %s rate-limited (429); parked %s, rotating", maskID(c.id), he.cooldown())
		case errors.As(err, &he) && (he.status == http.StatusUnauthorized || he.status == http.StatusForbidden):
			c.park(krogerParkDefault)
			log.Printf("kroger: credential %s auth failure (%d); parked, rotating", maskID(c.id), he.status)
		case errors.As(err, &he) && he.status >= 500:
			c.park(krogerParkDefault)
			log.Printf("kroger: credential %s server error (%d); parked, rotating", maskID(c.id), he.status)
		default:
			// Network error, context cancellation, parse failure: not a
			// credential problem. Don't thrash the rest of the pool.
			log.Printf("kroger: lookup error for %q: %v", term, err)
			return nil, nil
		}
	}
	return nil, nil
}

// lookupWith runs one product search on a single credential, transparently
// refreshing the token once if the API reports it invalid.
func (k *KrogerProvider) lookupWith(ctx context.Context, c *krogerCred, term string) (*PriceResult, error) {
	tok, err := k.getToken(ctx, c)
	if err != nil {
		return nil, err
	}

	res, staleToken, err := k.searchProducts(ctx, term, tok)
	if err != nil {
		return nil, err
	}
	if staleToken {
		c.clearToken()
		if tok, err = k.getToken(ctx, c); err != nil {
			return nil, err
		}
		if res, staleToken, err = k.searchProducts(ctx, term, tok); err != nil {
			return nil, err
		}
		if staleToken {
			return nil, &krogerHTTPError{status: http.StatusUnauthorized, body: "token rejected after refresh"}
		}
	}

	c.recordCall()
	return res, nil
}

// getToken returns a valid client-credentials access token for c, fetching a
// new one when the cache is empty or within the refresh skew.
func (k *KrogerProvider) getToken(ctx context.Context, c *krogerCred) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp.Add(-krogerTokenSkew)) {
		return c.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", "product.compact")
	body := form.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, krogerTokenURL, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }
	req.SetBasicAuth(c.id, c.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	k.limiter.Wait()
	resp, err := k.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", &krogerHTTPError{
			status:     resp.StatusCode,
			retryAfter: resp.Header.Get("Retry-After"),
			body:       string(snippet),
		}
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("kroger: token response carried no access_token")
	}

	ttl := tok.ExpiresIn
	if ttl <= 0 {
		ttl = 1800
	}
	c.token = tok.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.token, nil
}

// searchProducts performs one GET /v1/products call. The bool return is true
// when the API rejected the bearer token as invalid (caller should refresh and
// retry once). A non-200/401 response is returned as *krogerHTTPError.
func (k *KrogerProvider) searchProducts(ctx context.Context, term, token string) (*PriceResult, bool, error) {
	q := url.Values{}
	q.Set("filter.term", term)
	q.Set("filter.fulfillment", "ais")
	if k.locationID != "" {
		q.Set("filter.locationId", k.locationID)
	}
	q.Set("filter.limit", "5")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, krogerProductURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	k.limiter.Wait()
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if strings.Contains(string(snippet), "invalid_token") {
			return nil, true, nil
		}
		return nil, false, &krogerHTTPError{status: resp.StatusCode, body: string(snippet)}
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, false, &krogerHTTPError{
			status:     resp.StatusCode,
			retryAfter: resp.Header.Get("Retry-After"),
			body:       string(snippet),
		}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	return parseKrogerProducts(body), false, nil
}

// parseKrogerProducts returns the first product carrying a positive regular
// price, or nil when the payload has none.
func parseKrogerProducts(body []byte) *PriceResult {
	var payload struct {
		Data []struct {
			Description string `json:"description"`
			Items       []struct {
				Price struct {
					Regular float64 `json:"regular"`
				} `json:"price"`
				Size string `json:"size"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		return nil
	}

	for _, prod := range payload.Data {
		if len(prod.Items) == 0 {
			continue
		}
		item := prod.Items[0]
		if item.Price.Regular <= 0 {
			continue
		}
		packSize, unit := parseSize(item.Size)
		return &PriceResult{
			PriceCents:   int64(item.Price.Regular * 100),
			PurchaseUnit: unit,
			PackSize:     packSize,
			Source:       "live",
			Confidence:   ConfidenceLive,
			FetchedAt:    time.Now().UTC(),
		}
	}
	return nil
}

// krogerHTTPError carries a non-success status so Lookup can decide whether to
// park-and-rotate the credential.
type krogerHTTPError struct {
	status     int
	retryAfter string
	body       string
}

func (e *krogerHTTPError) Error() string {
	return fmt.Sprintf("kroger: http %d: %s", e.status, strings.TrimSpace(e.body))
}

// cooldown is how long to park a credential after this error: the server's
// Retry-After when present and sane, otherwise the default.
func (e *krogerHTTPError) cooldown() time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(e.retryAfter)); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > 5*time.Minute {
			d = 5 * time.Minute
		}
		return d
	}
	return krogerParkDefault
}

// maskID renders a client ID for logs without disclosing it.
func maskID(id string) string {
	if len(id) <= 4 {
		return "****"
	}
	return id[:2] + "…" + id[len(id)-2:]
}

// parseSize attempts to extract a numeric pack size and unit from strings like
// "5 lb", "12 oz", "6 count". Returns (1, "each") when parsing fails.
func parseSize(s string) (float64, string) {
	s = strings.ToLower(strings.TrimSpace(s))
	var size float64
	var unit string
	_, err := fmt.Sscanf(s, "%f %s", &size, &unit)
	if err != nil || size <= 0 {
		return 1, "each"
	}
	// Normalize common unit aliases.
	switch unit {
	case "lbs", "pound", "pounds":
		unit = "lb"
	case "ounce", "ounces", "oz.":
		unit = "oz"
	case "count", "ct", "pk", "pack", "packs":
		unit = "each"
	}
	return size, unit
}
