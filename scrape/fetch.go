// Package scrape provides server-side HTML fetching and product extraction
// for the configurable scraping engine (§6.7).
package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"goeat/safefetch"
)

const (
	// A modern grocery search page is routinely 1-2 MB of HTML, so the old
	// 512 KB cap silently truncated real pages - and, worse, truncated the
	// JSON a renderer wraps them in, which then failed to decode as
	// "unexpected EOF" and looked like the site had blocked us.
	maxBodyBytes = 5 * 1024 * 1024 // matches the documented SCRAPE_MAX_BYTES
	// A renderer returns that page inside a JSON envelope with cookies and
	// headers, so its response needs more headroom than the page itself.
	maxRenderBytes = 16 * 1024 * 1024
	fetchTimeout   = 10 * time.Second
)

// FetchResult is the raw HTML returned by Fetch or FetchViaProxy.
type FetchResult struct {
	HTML       string
	FinalURL   string
	StatusCode int
}

// Fetch performs a safe GET via safefetch (SSRF-guarded, size-capped).
func Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	r, err := safefetch.Fetch(ctx, rawURL, &safefetch.Options{
		MaxBytes: maxBodyBytes,
		Timeout:  fetchTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &FetchResult{
		HTML:       string(r.Body),
		FinalURL:   r.FinalURL,
		StatusCode: r.StatusCode,
	}, nil
}

// FetchViaProxy routes the request through a FlareSolverr instance when the
// direct fetch returns a challenge page (403/503). proxyURL is the FlareSolverr
// base URL, e.g. "http://localhost:8191".
func FetchViaProxy(ctx context.Context, targetURL, proxyURL string) (*FetchResult, error) {
	res, err := flareRequest(ctx, proxyURL, targetURL, "")
	if err != nil {
		return nil, err
	}
	if ChallengeReason(res.HTML, res.StatusCode) == "" {
		return res, nil
	}

	// Still a wall. Anti-bot suites hand out their clearance cookie on the
	// site's own front door and reject a cold deep link into search, so try
	// once more in a session that visits the origin first and carries the
	// cookies forward. Any failure here leaves the first result standing.
	warmed, werr := flareWarmSession(ctx, proxyURL, targetURL)
	if werr != nil || warmed == nil {
		// Worth a log line: from the caller's side this is indistinguishable
		// from "the wall held", and the two need different fixes.
		log.Printf("scrape: flaresolverr warm-up failed for %s: %v", targetURL, werr)
		return res, nil
	}
	return warmed, nil
}

// flareWarmSession runs origin-then-target inside one FlareSolverr session so
// the second request carries whatever cookies the first was issued.
func flareWarmSession(ctx context.Context, proxyURL, targetURL string) (*FetchResult, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("flaresolverr: unusable target URL")
	}
	origin := parsed.Scheme + "://" + parsed.Host + "/"

	session := fmt.Sprintf("goeat-%d", time.Now().UnixNano())
	if _, err := flareCommand(ctx, proxyURL, map[string]any{
		"cmd": "sessions.create", "session": session,
	}); err != nil {
		return nil, err
	}
	defer func() {
		// Sessions hold a browser open; a leaked one is a leaked Chrome.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_, _ = flareCommand(cleanup, proxyURL, map[string]any{
			"cmd": "sessions.destroy", "session": session,
		})
	}()

	if _, err := flareRequest(ctx, proxyURL, origin, session); err != nil {
		return nil, err
	}
	return flareRequest(ctx, proxyURL, targetURL, session)
}

// flareRequest issues one request.get, optionally inside a session.
func flareRequest(ctx context.Context, proxyURL, targetURL, session string) (*FetchResult, error) {
	payload := map[string]any{
		"cmd": "request.get",
		"url": targetURL,
		// An interstitial can take tens of seconds to clear; the old 15s
		// budget gave up while it was still working.
		"maxTimeout": 60000,
	}
	if session != "" {
		payload["session"] = session
	}
	out, err := flareCommand(ctx, proxyURL, payload)
	if err != nil {
		return nil, err
	}
	return &FetchResult{
		HTML:       out.Solution.Response,
		FinalURL:   out.Solution.URL,
		StatusCode: out.Solution.Status,
	}, nil
}

type flareResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL       string   `json:"url"`
		Response  string   `json:"response"`
		Status    int      `json:"status"`
		UserAgent string   `json:"userAgent"`
		Cookies   []Cookie `json:"cookies"`
	} `json:"solution"`
}

// Cookie is one cookie as FlareSolverr reports it, in the shape Browserless
// (Puppeteer) wants back. The clearance cookie an anti-bot service issues is
// the whole point: it is what makes a second, JavaScript-capable browser look
// like an already-vetted visitor.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`

	// FlareSolverr calls it "expiry"; Puppeteer calls it "expires".
	Expiry float64 `json:"expiry,omitempty"`
}

// Clearance holds what one browser learned about a site so another can reuse
// it: the cookies it was issued and the user agent they were issued to.
type Clearance struct {
	Cookies   []Cookie
	UserAgent string
}

// FlareClearance visits a site through FlareSolverr - front door first, then
// the target - and returns the cookies it came away with, without caring what
// the HTML said. Browserless can then render the page as a vetted visitor.
func FlareClearance(ctx context.Context, proxyURL, targetURL string) (*Clearance, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("flaresolverr: unusable target URL")
	}
	origin := parsed.Scheme + "://" + parsed.Host + "/"

	session := fmt.Sprintf("goeat-clear-%d", time.Now().UnixNano())
	if _, err := flareCommand(ctx, proxyURL, map[string]any{
		"cmd": "sessions.create", "session": session,
	}); err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_, _ = flareCommand(cleanup, proxyURL, map[string]any{
			"cmd": "sessions.destroy", "session": session,
		})
	}()

	if _, err := flareCommand(ctx, proxyURL, map[string]any{
		"cmd": "request.get", "url": origin, "session": session, "maxTimeout": 60000,
	}); err != nil {
		return nil, err
	}
	out, err := flareCommand(ctx, proxyURL, map[string]any{
		"cmd": "request.get", "url": targetURL, "session": session, "maxTimeout": 60000,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Solution.Cookies) == 0 {
		return nil, fmt.Errorf("flaresolverr: no cookies to hand over")
	}
	cookies := make([]Cookie, 0, len(out.Solution.Cookies))
	for _, c := range out.Solution.Cookies {
		if c.Expires == 0 && c.Expiry != 0 {
			c.Expires = c.Expiry
		}
		c.Expiry = 0
		cookies = append(cookies, c)
	}
	return &Clearance{Cookies: cookies, UserAgent: out.Solution.UserAgent}, nil
}

// FetchWithSavedClearance retries rawURL through Browserless carrying cookies
// an admin obtained earlier (live-solve or pasted manually), rather than
// running FlareSolverr again. Reports ok only when the result is a real page
// - a caller should fall through to the ordinary FetchSmart chain, and drop
// the saved clearance, otherwise (it has expired or the site no longer
// accepts it).
func FetchWithSavedClearance(ctx context.Context, rawURL string, browserless Renderer, cl *Clearance) (*FetchResult, bool) {
	if !browserless.Enabled() || cl == nil || len(cl.Cookies) == 0 {
		return nil, false
	}
	rendered, err := RenderViaWith(ctx, browserless, rawURL, cl)
	if err != nil {
		log.Printf("scrape: render with saved clearance failed for %s: %v", rawURL, err)
		return nil, false
	}
	if ChallengeReason(rendered.HTML, rendered.StatusCode) != "" {
		return nil, false
	}
	return rendered, true
}

// flareCommand posts one FlareSolverr command to /v1.
func flareCommand(ctx context.Context, proxyURL string, payload map[string]any) (*flareResponse, error) {
	b, _ := json.Marshal(payload)
	// Accept both "http://host:8191" and "http://host:8191/v1" - the older
	// .env example documented the /v1 form, and doubling it 404s.
	endpoint := strings.TrimRight(strings.TrimSpace(proxyURL), "/")
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint, strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Longer than the 60s maxTimeout above, or the client would hang up on a
	// challenge FlareSolverr is still solving.
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out flareResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRenderBytes)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Status != "ok" {
		if out.Message != "" {
			return nil, fmt.Errorf("flaresolverr: %s", out.Message)
		}
		return nil, fmt.Errorf("flaresolverr: status=%q", out.Status)
	}
	return &out, nil
}

// CookieDomain is the host a cookie should be pinned to, as a leading-dot
// domain so it also covers the site's subdomains.
func CookieDomain(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := parsed.Hostname()
	if strings.HasPrefix(host, "www.") {
		return "." + strings.TrimPrefix(host, "www.")
	}
	return host
}
