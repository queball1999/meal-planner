package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// krogerFake stands in for api.kroger.com. It issues per-credential bearer
// tokens ("tok-<clientID>") and lets each test script the /products response.
type krogerFake struct {
	srv *httptest.Server

	tokenHits   int64
	productHits int64

	mu      sync.Mutex
	product func(clientToken string, n int64) (status int, headers map[string]string, body string)
}

func newKrogerFake(t *testing.T) *krogerFake {
	t.Helper()
	f := &krogerFake{}
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.tokenHits, 1)
		user, _, _ := r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-" + user,
			"expires_in":   1800,
			"token_type":   "bearer",
		})
	})

	mux.HandleFunc("/products", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&f.productHits, 1)
		tokenHdr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		fn := f.product
		f.mu.Unlock()
		status, headers, body := 200, map[string]string(nil), pricedBody(499)
		if fn != nil {
			status, headers, body = fn(tokenHdr, n)
		}
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	// Redirect the package endpoints at the fake for the duration of the test.
	oldTok, oldProd := krogerTokenURL, krogerProductURL
	krogerTokenURL = f.srv.URL + "/token"
	krogerProductURL = f.srv.URL + "/products"
	t.Cleanup(func() { krogerTokenURL, krogerProductURL = oldTok, oldProd })

	return f
}

func (f *krogerFake) setProduct(fn func(clientToken string, n int64) (int, map[string]string, string)) {
	f.mu.Lock()
	f.product = fn
	f.mu.Unlock()
}

func pricedBody(cents int) string {
	return fmt.Sprintf(`{"data":[{"description":"Test Item","items":[{"price":{"regular":%.2f},"size":"12 oz"}]}]}`, float64(cents)/100)
}

func withMaxRetries(t *testing.T, n int) {
	t.Helper()
	old := krogerMaxRetries
	krogerMaxRetries = n
	t.Cleanup(func() { krogerMaxRetries = old })
}

func TestKroger_TokenCachedAcrossLookups(t *testing.T) {
	f := newKrogerFake(t)
	f.setProduct(func(_ string, _ int64) (int, map[string]string, string) {
		return 200, nil, pricedBody(499)
	})

	k := NewKrogerProvider([][2]string{{"id-one", "secret"}}, "0100", 60, 9500)
	if k == nil {
		t.Fatal("provider is nil")
	}

	for i := 0; i < 3; i++ {
		res, err := k.Lookup(context.Background(), "milk", 0, "")
		if err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if res == nil || res.PriceCents != 499 {
			t.Fatalf("lookup %d: got %+v", i, res)
		}
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 1 {
		t.Fatalf("token fetched %d times, want 1 (should be cached)", got)
	}
	if got := atomic.LoadInt64(&f.productHits); got != 3 {
		t.Fatalf("product called %d times, want 3", got)
	}
}

func TestKroger_RefreshOnInvalidToken(t *testing.T) {
	f := newKrogerFake(t)
	f.setProduct(func(_ string, n int64) (int, map[string]string, string) {
		if n == 1 {
			return 401, nil, `{"error":"invalid_token","error_description":"expired"}`
		}
		return 200, nil, pricedBody(1299)
	})

	k := NewKrogerProvider([][2]string{{"id-one", "secret"}}, "", 60, 9500)
	res, err := k.Lookup(context.Background(), "eggs", 0, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res == nil || res.PriceCents != 1299 {
		t.Fatalf("got %+v", res)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 2 {
		t.Fatalf("token fetched %d times, want 2 (initial + refresh)", got)
	}
}

func TestKroger_RetryOn429WithRetryAfter(t *testing.T) {
	withMaxRetries(t, 3)
	f := newKrogerFake(t)
	f.setProduct(func(_ string, n int64) (int, map[string]string, string) {
		if n == 1 {
			return 429, map[string]string{"Retry-After": "1"}, `{"error":"rate limit"}`
		}
		return 200, nil, pricedBody(250)
	})

	k := NewKrogerProvider([][2]string{{"id-one", "secret"}}, "", 60, 9500)
	start := time.Now()
	res, err := k.Lookup(context.Background(), "flour", 0, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res == nil || res.PriceCents != 250 {
		t.Fatalf("got %+v", res)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("returned after %s, expected to honour Retry-After: 1", elapsed)
	}
	if got := atomic.LoadInt64(&f.productHits); got != 2 {
		t.Fatalf("product called %d times, want 2", got)
	}
}

func TestKroger_RotatesToSecondCredentialOn429(t *testing.T) {
	withMaxRetries(t, 0) // no in-transport retries: a 429 surfaces immediately
	f := newKrogerFake(t)
	f.setProduct(func(clientToken string, _ int64) (int, map[string]string, string) {
		if clientToken == "tok-id-a" {
			return 429, nil, `{"error":"rate limit"}`
		}
		return 200, nil, pricedBody(777)
	})

	k := NewKrogerProvider([][2]string{{"id-a", "sa"}, {"id-b", "sb"}}, "", 60, 9500)
	res, err := k.Lookup(context.Background(), "butter", 0, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res == nil || res.PriceCents != 777 {
		t.Fatalf("got %+v", res)
	}

	// id-a must now be parked; a second lookup should go straight to id-b and
	// not touch id-a at all.
	before := atomic.LoadInt64(&f.productHits)
	if _, err := k.Lookup(context.Background(), "butter", 0, ""); err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if got := atomic.LoadInt64(&f.productHits) - before; got != 1 {
		t.Fatalf("second lookup made %d product calls, want 1 (id-a parked)", got)
	}
}

func TestKroger_SkipsCredentialOverDailyCap(t *testing.T) {
	f := newKrogerFake(t)
	f.setProduct(func(clientToken string, _ int64) (int, map[string]string, string) {
		if clientToken == "tok-id-a" {
			t.Errorf("id-a should be skipped (over daily cap)")
			return 500, nil, ""
		}
		return 200, nil, pricedBody(300)
	})

	k := NewKrogerProvider([][2]string{{"id-a", "sa"}, {"id-b", "sb"}}, "", 60, 5)
	// Pre-exhaust id-a's daily budget.
	k.creds[0].mu.Lock()
	k.creds[0].dayStart = time.Now()
	k.creds[0].dayCount = 5
	k.creds[0].mu.Unlock()

	res, err := k.Lookup(context.Background(), "sugar", 0, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res == nil || res.PriceCents != 300 {
		t.Fatalf("got %+v", res)
	}
}

func TestKroger_AllCredentialsParkedFailsSoft(t *testing.T) {
	withMaxRetries(t, 0)
	f := newKrogerFake(t)
	f.setProduct(func(_ string, _ int64) (int, map[string]string, string) {
		return 429, nil, `{"error":"rate limit"}`
	})

	k := NewKrogerProvider([][2]string{{"id-a", "sa"}}, "", 60, 9500)
	res, err := k.Lookup(context.Background(), "x", 0, "")
	if err != nil || res != nil {
		t.Fatalf("want (nil,nil) fail-soft, got res=%+v err=%v", res, err)
	}
	// Now every credential is parked; the next lookup returns without any HTTP.
	before := atomic.LoadInt64(&f.productHits)
	res, err = k.Lookup(context.Background(), "x", 0, "")
	if err != nil || res != nil {
		t.Fatalf("want (nil,nil), got res=%+v err=%v", res, err)
	}
	if got := atomic.LoadInt64(&f.productHits) - before; got != 0 {
		t.Fatalf("made %d HTTP calls with all creds parked, want 0", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in       string
		wantSize float64
		wantUnit string
	}{
		{"12 oz", 12, "oz"},
		{"5 lb", 5, "lb"},
		{"5 lbs", 5, "lb"},
		{"6 count", 6, "each"},
		{"1.5 ounces", 1.5, "oz"},
		{"", 1, "each"},
		{"family size", 1, "each"},
	}
	for _, c := range cases {
		size, unit := parseSize(c.in)
		if size != c.wantSize || unit != c.wantUnit {
			t.Errorf("parseSize(%q) = (%v, %q), want (%v, %q)", c.in, size, unit, c.wantSize, c.wantUnit)
		}
	}
}

func TestDeriveKrogerCredentialsShape(t *testing.T) {
	// Guards the config-side contract this provider depends on: a bare pair and
	// a comma-separated pool both parse position-for-position.
	got := parseCreds("a", "sa")
	if len(got) != 1 || got[0] != [2]string{"a", "sa"} {
		t.Fatalf("single: %v", got)
	}
	got = parseCreds("a, b ,c", "sa,sb,sc")
	if len(got) != 3 || got[2] != [2]string{"c", "sc"} {
		t.Fatalf("multi: %v", got)
	}
	got = parseCreds("a,b", "sa")
	if len(got) != 1 {
		t.Fatalf("imbalanced should keep 1 pair: %v", got)
	}
}

// parseCreds mirrors config.Config.DeriveKrogerCredentials for a package-local
// unit test without importing the config package.
func parseCreds(ids, secrets string) [][2]string {
	split := func(s string) []string {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		p := strings.Split(s, ",")
		for i := range p {
			p[i] = strings.TrimSpace(p[i])
		}
		return p
	}
	id, sec := split(ids), split(secrets)
	n := len(id)
	if len(sec) < n {
		n = len(sec)
	}
	out := make([][2]string, 0, n)
	for i := 0; i < n; i++ {
		if id[i] == "" || sec[i] == "" {
			continue
		}
		out = append(out, [2]string{id[i], sec[i]})
	}
	return out
}
