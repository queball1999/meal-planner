package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"goeat/config"
	"goeat/db"
)

func newCSRFTestHandler(t *testing.T, publicBaseURL string) http.Handler {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppName: "test", SessionSecret: strings.Repeat("k", 32), SessionTTLHours: 1, AutoPlanHour: -1, PublicBaseURL: publicBaseURL}
	return NewServer(cfg, store, nil, "test", nil).handler
}

// TestCSRFCrossOrigin covers QSS security design §6: every state-changing
// request is origin-checked, the login form included (login CSRF), while
// same-origin and non-browser requests pass.
func TestCSRFCrossOrigin(t *testing.T) {
	h := newCSRFTestHandler(t, "")
	form := url.Values{"username": {"x"}, "password": {"y"}}.Encode()

	cases := []struct {
		name    string
		headers map[string]string
		blocked bool
	}{
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"sibling subdomain", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"old browser, foreign Origin", map[string]string{"Origin": "https://evil.example"}, true},
		{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"non-browser client", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://goeat.local/auth/login", strings.NewReader(form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if blocked := w.Code == http.StatusForbidden; blocked != c.blocked {
				t.Errorf("status %d, blocked = %v, want %v", w.Code, blocked, c.blocked)
			}
		})
	}
}

// TestCSRFSchemeGuard: with PUBLIC_BASE_URL on https, a POST whose Origin is
// the same host over http is refused, which the host-only fallback can't see.
func TestCSRFSchemeGuard(t *testing.T) {
	h := newCSRFTestHandler(t, "https://goeat.example")

	cases := []struct {
		name, origin, referer string
		blocked               bool
	}{
		{"http origin, same host", "http://goeat.example", "", true},
		{"http referer, same host", "", "http://goeat.example/plan", true},
		{"https origin passes the guard", "https://goeat.example", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://goeat.example/auth/login", strings.NewReader("username=x&password=y"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.referer != "" {
				r.Header.Set("Referer", c.referer)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if blocked := w.Code == http.StatusForbidden; blocked != c.blocked {
				t.Errorf("status %d, blocked = %v, want %v", w.Code, blocked, c.blocked)
			}
		})
	}

	// GETs are never guarded.
	r := httptest.NewRequest("GET", "https://goeat.example/health", nil)
	r.Header.Set("Origin", "http://goeat.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET /health with http Origin = %d, want 200", w.Code)
	}
}
