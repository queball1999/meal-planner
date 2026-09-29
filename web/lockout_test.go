package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"goeat/auth"
	"goeat/middleware"
)

// login posts the sign-in form from ip and reports whether a session cookie
// came back, plus the notify message shown.
func login(h http.Handler, user, pass, ip string) (signedIn bool, notice string) {
	form := url.Values{"username": {user}, "password": {pass}}
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case middleware.SessionCookieName:
			signedIn = c.Value != ""
		case notifyCookieName:
			_, msg, _ := strings.Cut(c.Value, "|")
			notice, _ = url.QueryUnescape(msg)
		}
	}
	return signedIn, notice
}

// TestLoginLockout covers QSS security design §2.1-§2.2.
func TestLoginLockout(t *testing.T) {
	s, store, h := newSetupTestServer(t, "")
	ctx := context.Background()
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(ctx, "Owner", hash, "admin"); err != nil {
		t.Fatal(err)
	}

	// Five wrong passwords from one address lock that (username, IP) pair:
	// the right password is refused there, and the key is case-insensitive.
	for i := 0; i < pairMax; i++ {
		if ok, _ := login(h, "Owner", "wrong", "10.0.0.1"); ok {
			t.Fatal("wrong password signed in")
		}
	}
	if ok, notice := login(h, "owner", "correct horse battery", "10.0.0.1"); ok || !strings.Contains(notice, "Try again in") {
		t.Fatalf("locked pair: signedIn=%v notice=%q", ok, notice)
	}

	// The owner on another device is unaffected - no hard lock on the
	// username alone (it only slows down).
	if ok, notice := login(h, "Owner", "correct horse battery", "10.0.0.2"); !ok {
		t.Fatalf("other IP should still sign in, notice=%q", notice)
	}

	// A username that doesn't exist locks the same way, so the lockout
	// can't be used to find out which accounts exist.
	for i := 0; i < pairMax; i++ {
		login(h, "nobody", "wrong", "10.0.0.3")
	}
	if _, notice := login(h, "nobody", "wrong", "10.0.0.3"); !strings.Contains(notice, "Try again in") {
		t.Fatalf("unknown username not locked, notice=%q", notice)
	}

	// The lock is in the database: a fresh limiter (a restart) doesn't
	// clear it.
	s.loginLimiter = newAttemptLimiter(loginRateMax, time.Minute)
	if ok, _ := login(h, "owner", "correct horse battery", "10.0.0.1"); ok {
		t.Fatal("lock didn't survive a restart")
	}

	// Per-IP rate limit: loginRateMax attempts a minute, whatever the
	// username.
	for i := 0; i < loginRateMax; i++ {
		login(h, "user"+string(rune('a'+i)), "wrong", "10.0.0.4")
	}
	if _, notice := login(h, "someone-else", "wrong", "10.0.0.4"); !strings.Contains(notice, "Wait a minute") {
		t.Fatalf("rate limit not applied, notice=%q", notice)
	}
}

func TestLockedUntil(t *testing.T) {
	now := time.Now()
	burst := []time.Time{now, now.Add(-time.Minute), now.Add(-2 * time.Minute), now.Add(-3 * time.Minute), now.Add(-4 * time.Minute)}
	if got := lockedUntil(burst, 5); !got.Equal(now.Add(lockFor)) {
		t.Errorf("5 failures in 4 minutes: locked until %v, want %v", got, now.Add(lockFor))
	}
	spread := []time.Time{now, now.Add(-2 * time.Minute), now.Add(-4 * time.Minute), now.Add(-6 * time.Minute), now.Add(-8 * time.Minute)}
	if got := lockedUntil(spread, 5); !got.IsZero() {
		t.Errorf("5 failures over 8 minutes shouldn't lock, got %v", got)
	}
	if got := lockedUntil(burst[:4], 5); !got.IsZero() {
		t.Errorf("4 failures shouldn't lock, got %v", got)
	}
}
