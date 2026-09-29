package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// ── First-run claim (QSS security design §15) ───────────────────────────────
//
// Until the first account exists, whoever reaches /setup first would own the
// instance - its admin, AI keys and every household. The wizard therefore
// demands a token only the operator can see: SETUP_TOKEN from the
// environment, or a random one printed once to stderr at startup. It lives in
// memory only and dies the moment setup completes, so an old log line is
// worthless afterwards.

// setupClaim holds the armed token and serialises claims, so two correct
// submissions racing each other can't both create an admin.
type setupClaim struct {
	mu    sync.Mutex
	token string // "" = not armed: setup is closed (fail closed)
}

// ArmSetupToken arms the first-run token when no account exists yet, printing
// it to w (stderr in production) on a line by itself. A no-op once setup is
// done. Called from main before serving.
func (s *Server) ArmSetupToken(ctx context.Context, w io.Writer) error {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("setup token: count users: %w", err)
	}
	if n > 0 {
		return nil
	}
	tok := s.cfg.SetupToken
	if tok == "" {
		b := make([]byte, 16) // 128 bits
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("setup token: %w", err)
		}
		tok = hex.EncodeToString(b)
		fmt.Fprintf(w, "Setup token: %s\n", tok)
	} else {
		fmt.Fprintln(w, "Setup token: using SETUP_TOKEN from the environment")
	}
	s.setup.mu.Lock()
	s.setup.token = tok
	s.setup.mu.Unlock()
	return nil
}

// setupTokenMatches compares in constant time. Caller holds s.setup.mu.
func (s *Server) setupTokenMatches(given string) bool {
	want := s.setup.token
	return want != "" && subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}

// ── Per-IP attempt limiter (QSS §2.1: in-memory, per-IP, transient) ─────────

// attemptLimiter allows max attempts per key within window.
type attemptLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string][]time.Time
}

func newAttemptLimiter(max int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: max, window: window, seen: map[string][]time.Time{}}
}

// allow records an attempt for key and reports whether it is within budget.
func (l *attemptLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.seen[key] = kept
		return false
	}
	l.seen[key] = append(kept, now)
	return true
}
