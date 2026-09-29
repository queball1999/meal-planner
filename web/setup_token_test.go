package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"goeat/config"
	"goeat/db"
	"goeat/middleware"
)

func newSetupTestServer(t *testing.T, envToken string) (*Server, db.Store, http.Handler) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppName: "test", SessionSecret: strings.Repeat("k", 32), SessionTTLHours: 1, AutoPlanHour: -1, SetupToken: envToken}
	s := NewServer(cfg, store, nil, "test", nil)
	mux := http.NewServeMux()
	s.routes(mux)
	return s, store, middleware.LoadSession(store, false)(mux)
}

func postSetup(h http.Handler, token, ip string) *httptest.ResponseRecorder {
	form := url.Values{
		"setup_token": {token}, "username": {"owner"}, "password": {"correct horse battery"},
		"password_confirm": {"correct horse battery"}, "budget": {"150"}, "household_size": {"2"},
		"timezone": {"America/New_York"},
	}
	r := httptest.NewRequest("POST", "/setup", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestSetupTokenClaim covers QSS security design §15 end to end: a wrong or
// missing token can't claim the instance, the printed token can, and it dies
// the moment it's used.
func TestSetupTokenClaim(t *testing.T) {
	s, store, h := newSetupTestServer(t, "")
	ctx := context.Background()

	// Not armed yet: setup is closed, even with an empty token.
	postSetup(h, "", "10.0.0.1")
	if n, _ := store.CountUsers(ctx); n != 0 {
		t.Fatal("setup succeeded with no token armed")
	}

	var log bytes.Buffer
	if err := s.ArmSetupToken(ctx, &log); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^Setup token: ([0-9a-f]{32})$`).FindStringSubmatch(log.String())
	if m == nil {
		t.Fatalf("no token line printed: %q", log.String())
	}
	token := m[1]

	postSetup(h, "0123456789abcdef0123456789abcdef", "10.0.0.2")
	if n, _ := store.CountUsers(ctx); n != 0 {
		t.Fatal("setup succeeded with a wrong token")
	}

	if w := postSetup(h, token, "10.0.0.3"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("correct token: %d -> %q", w.Code, w.Header().Get("Location"))
	}
	u, _ := store.GetUserByUsername(ctx, "owner")
	if u == nil || !u.IsAdmin() {
		t.Fatalf("claimed account = %+v, want an admin", u)
	}
	if ms, _ := store.ListMembershipsForUser(ctx, u.ID); len(ms) != 1 || ms[0].Role != db.HouseholdRoleOwner {
		t.Errorf("claimer memberships = %+v, want owner of the new household", ms)
	}

	// The token is spent: a second claim with it does nothing.
	s.setup.mu.Lock()
	spent := s.setup.token == ""
	s.setup.mu.Unlock()
	if !spent {
		t.Error("token still armed after setup completed")
	}

	// Once an account exists, arming is a no-op and prints nothing.
	log.Reset()
	if err := s.ArmSetupToken(ctx, &log); err != nil || log.Len() != 0 {
		t.Errorf("re-arm after setup: err=%v printed=%q", err, log.String())
	}
}

func TestSetupTokenFromEnv(t *testing.T) {
	const envTok = "aabbccddeeff00112233445566778899"
	s, store, h := newSetupTestServer(t, envTok)
	var log bytes.Buffer
	if err := s.ArmSetupToken(context.Background(), &log); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), envTok) {
		t.Error("SETUP_TOKEN from the environment was echoed to the log")
	}
	postSetup(h, envTok, "10.0.0.9")
	if n, _ := store.CountUsers(context.Background()); n != 1 {
		t.Errorf("setup with SETUP_TOKEN: %d users, want 1", n)
	}
}

// TestSetupTokenRateLimit: guesses are throttled per IP (QSS §2.1).
func TestSetupTokenRateLimit(t *testing.T) {
	s, _, h := newSetupTestServer(t, "")
	if err := s.ArmSetupToken(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if w := postSetup(h, "wrong", "10.0.0.5"); w.Code == http.StatusTooManyRequests {
			t.Fatalf("throttled after only %d attempts", i)
		}
	}
	if w := postSetup(h, "wrong", "10.0.0.5"); w.Code != http.StatusTooManyRequests {
		t.Errorf("11th attempt: %d, want 429", w.Code)
	}
	if w := postSetup(h, "wrong", "10.0.0.6"); w.Code == http.StatusTooManyRequests {
		t.Error("a different IP was throttled")
	}
}

func TestAttemptLimiterWindow(t *testing.T) {
	l := newAttemptLimiter(2, time.Minute)
	now := time.Now()
	if !l.allow("a", now) || !l.allow("a", now) || l.allow("a", now) {
		t.Fatal("limit of 2 not enforced")
	}
	if !l.allow("a", now.Add(61*time.Second)) {
		t.Error("attempts didn't expire after the window")
	}
}
