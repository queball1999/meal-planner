// Package middleware provides HTTP middleware: session loading, auth gates,
// and IP extraction. Context keys are defined here so both middleware and web
// handlers share them without a circular import.
package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"goeat/auth"
	"goeat/db"
)

// SessionCookieName is the name of the session cookie, shared with the web
// package so both read and write the same cookie.
const SessionCookieName = "goeat_session"

// ── Context keys ────────────────────────────────────────────────────────────

type ctxKey int

const (
	ctxKeyUser      ctxKey = iota
	ctxKeyHousehold ctxKey = iota
	ctxKeyReqStart  ctxKey = iota
)

// UserFromCtx returns the authenticated user from the context, or nil.
func UserFromCtx(r *http.Request) *db.User {
	u, _ := r.Context().Value(ctxKeyUser).(*db.User)
	return u
}

// HouseholdFromCtx returns the household from the context, or nil if setup
// has not been completed.
func HouseholdFromCtx(r *http.Request) *db.Household {
	h, _ := r.Context().Value(ctxKeyHousehold).(*db.Household)
	return h
}

// ── Timing ───────────────────────────────────────────────────────────────────

// Timing stamps the request's arrival time into context so render() can later
// compute page-load wall-clock time for the footer. Wrap the outermost layer
// of the handler chain so the measurement includes CSRF and session overhead,
// not just the route handler's own work.
func Timing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyReqStart, time.Now())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestStart returns when Timing saw this request begin.
func RequestStart(r *http.Request) (time.Time, bool) {
	t, ok := r.Context().Value(ctxKeyReqStart).(time.Time)
	return t, ok
}

// ── LoadSession ──────────────────────────────────────────────────────────────

// LoadSession reads the session cookie, validates the token, and injects the
// authenticated user and household into the request context. Applied to the
// entire mux so every handler can read the user without repeating the lookup.
func LoadSession(store db.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			cookie, err := r.Cookie(SessionCookieName)
			if err == nil && cookie.Value != "" {
				tokenHash := auth.HashToken(cookie.Value)
				sess, err := store.GetSessionByTokenHash(ctx, tokenHash)
				if err == nil && sess != nil && time.Now().Before(sess.ExpiresAt) {
					user, err := store.GetUserByID(ctx, sess.UserID)
					if err == nil && user != nil {
						ctx = context.WithValue(ctx, ctxKeyUser, user)
					}
				}
			}

			// Always try to load household - needed for setup-redirect logic.
			hh, err := store.GetHousehold(ctx)
			if err == nil && hh != nil {
				ctx = context.WithValue(ctx, ctxKeyHousehold, hh)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ── RequireAuth ──────────────────────────────────────────────────────────────

// RequireAuth redirects to /auth/login when no user is present in the context.
// Apply to any route that requires authentication.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromCtx(r) == nil {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── ClientIP ─────────────────────────────────────────────────────────────────

// ClientIP extracts the real client IP from the request, following the chain
// X-Forwarded-For → X-Real-IP → RemoteAddr (§9.3).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		return r.RemoteAddr
	}
	return host
}
