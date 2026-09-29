package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"goeat/auth"
	"goeat/config"
	"goeat/db"
	"goeat/middleware"
)

func newHeadersTestServer(t *testing.T, publicURL string, allowed ...string) *Server {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppName: "test", SessionSecret: strings.Repeat("k", 32), SessionTTLHours: 1,
		AutoPlanHour: -1, PublicBaseURL: publicURL, AllowedHosts: allowed}
	return NewServer(cfg, store, nil, "test", nil)
}

// TestSecurityHeaders covers QSS security design §10: the full header set,
// and a CSP nonce that matches the one on the page's inline scripts.
func TestSecurityHeaders(t *testing.T) {
	s := newHeadersTestServer(t, "https://goeat.example")
	r := httptest.NewRequest("GET", "/setup", nil)
	r.Host = "goeat.example"
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)

	h := w.Header()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	scriptSrc := regexp.MustCompile(`script-src ([^;]*)`).FindStringSubmatch(csp)
	if scriptSrc == nil || strings.Contains(scriptSrc[1], "'unsafe-inline'") {
		t.Errorf("script-src must use a nonce, not 'unsafe-inline': %s", csp)
	}
	nonce := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(csp)
	if nonce == nil {
		t.Fatalf("no nonce in CSP: %s", csp)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<script nonce="`+nonce[1]+`">`) {
		t.Error("page's inline scripts don't carry the CSP nonce")
	}
	if strings.Contains(body, "<script>") {
		t.Error("an inline <script> has no nonce - the CSP will block it")
	}
	for k, want := range map[string]string{
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// TestHostGuard covers QSS security design §9.3: names must be configured,
// IP literals and localhost always work.
func TestHostGuard(t *testing.T) {
	s := newHeadersTestServer(t, "http://goeat.lan:8080", "pantry.home")
	for host, want := range map[string]bool{
		"goeat.lan:8080":     true,
		"pantry.home:8080":   true, // listed without a port: any port
		"192.168.1.20:8080":  true,
		"[fe80::1]:8080":     true,
		"localhost:8080":     true,
		"evil.example":       false,
		"goeat.lan.evil.com": false,
		"goeat.lan:9999":     false, // listed with a port: that port only
	} {
		r := httptest.NewRequest("GET", "/auth/login", nil)
		r.Host = host
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if got := w.Code != http.StatusMisdirectedRequest; got != want {
			t.Errorf("Host %q: allowed=%v, want %v (status %d)", host, got, want, w.Code)
		}
	}
}

// TestSessionIdleTimeout covers QSS security design §1.4: a session idle past
// the limit is refused (and deleted), and background requests don't count as
// activity.
func TestSessionIdleTimeout(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, err := store.CreateUser(ctx, "owner", "unused", "admin")
	if err != nil {
		t.Fatal(err)
	}
	newSession := func(token string, lastSeen time.Time) *db.Session {
		sess, err := store.CreateSession(ctx, u.ID, auth.HashToken(token), "", "", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.TouchSession(ctx, sess.ID, lastSeen); err != nil {
			t.Fatal(err)
		}
		return sess
	}

	var signedIn bool
	h := middleware.LoadSession(store, middleware.SessionOptions{IdleTimeout: 30 * time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			signedIn = middleware.UserFromCtx(r) != nil
		}))
	get := func(token, mode string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: token})
		r.Header.Set("Sec-Fetch-Mode", mode)
		h.ServeHTTP(httptest.NewRecorder(), r)
		return signedIn
	}

	newSession("idle", time.Now().Add(-31*time.Minute))
	if get("idle", "navigate") {
		t.Error("session idle for 31 minutes was accepted")
	}
	if sess, _ := store.GetSessionByTokenHash(ctx, auth.HashToken("idle")); sess != nil {
		t.Error("idle session wasn't deleted")
	}

	active := newSession("active", time.Now().Add(-10*time.Minute))
	if !get("active", "cors") {
		t.Fatal("active session refused")
	}
	if sess, _ := store.GetSessionByTokenHash(ctx, auth.HashToken("active")); sess.LastSeenAt.After(active.CreatedAt.Add(time.Minute)) {
		t.Error("a background (cors) request refreshed last_seen_at")
	}
	get("active", "navigate")
	if sess, _ := store.GetSessionByTokenHash(ctx, auth.HashToken("active")); time.Since(sess.LastSeenAt) > time.Minute {
		t.Error("a page load didn't refresh last_seen_at")
	}
}
