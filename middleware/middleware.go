// Package middleware provides HTTP middleware: session loading, auth gates,
// and IP extraction. Context keys are defined here so both middleware and web
// handlers share them without a circular import.
package middleware

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"goeat/auth"
	"goeat/db"
	"goeat/llm"
)

// SessionCookieName is the name of the session cookie, shared with the web
// package so both read and write the same cookie.
const SessionCookieName = "goeat_session"

// ── Context keys ────────────────────────────────────────────────────────────

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyHousehold
	ctxKeyReqStart
	ctxKeyRole
	ctxKeySession
	ctxKeyMemberships
	ctxKeySetupDone
)

// UserFromCtx returns the authenticated user from the context, or nil.
func UserFromCtx(r *http.Request) *db.User {
	u, _ := r.Context().Value(ctxKeyUser).(*db.User)
	return u
}

// HouseholdFromCtx returns the session's active household, or nil when the
// request is anonymous or the user belongs to no household.
func HouseholdFromCtx(r *http.Request) *db.Household {
	h, _ := r.Context().Value(ctxKeyHousehold).(*db.Household)
	return h
}

// HouseholdRoleFromCtx returns the user's effective role in the active
// household ("" when there is none). Instance admins read as owner.
func HouseholdRoleFromCtx(r *http.Request) string {
	role, _ := r.Context().Value(ctxKeyRole).(string)
	return role
}

// SessionFromCtx returns the validated session row, or nil when anonymous.
func SessionFromCtx(r *http.Request) *db.Session {
	s, _ := r.Context().Value(ctxKeySession).(*db.Session)
	return s
}

// MembershipsFromCtx returns every household the user belongs to - the
// household switcher's list.
func MembershipsFromCtx(r *http.Request) []*db.HouseholdMembership {
	m, _ := r.Context().Value(ctxKeyMemberships).([]*db.HouseholdMembership)
	return m
}

// SetupDone reports whether the first-run wizard has created an account.
func SetupDone(r *http.Request) bool {
	done, _ := r.Context().Value(ctxKeySetupDone).(bool)
	return done
}

// CanEdit / CanOwn are the template-facing forms of the role gates. They only
// decide what to show; RequireHouseholdRole is what enforces.
func CanEdit(r *http.Request) bool {
	return db.HouseholdRoleRank(HouseholdRoleFromCtx(r)) >= db.HouseholdRoleRank(db.HouseholdRoleEditor)
}

func CanOwn(r *http.Request) bool {
	return db.HouseholdRoleRank(HouseholdRoleFromCtx(r)) >= db.HouseholdRoleRank(db.HouseholdRoleOwner)
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
// authenticated user, their active household and their role in it. Applied to
// the entire mux so every handler can read them without repeating the lookup.
//
// The user and role are re-read from the database on every request, never
// copied from login time, so a demotion or a removal from a household takes
// effect on the victim's very next click (QSS security design §1.4, §8.5).
//
// multiTenant is ENABLE_MULTI_TENANT: when false the instance has one
// household (the oldest) and every request acts on it - see
// withActiveHousehold.
func LoadSession(store db.Store, multiTenant bool) func(http.Handler) http.Handler {
	// Once an account exists setup is done for good (the last admin can't be
	// deleted), so stop counting users after the first yes.
	var setupDone atomic.Bool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			if !setupDone.Load() {
				if n, err := store.CountUsers(ctx); err == nil && n > 0 {
					setupDone.Store(true)
				}
			}
			ctx = context.WithValue(ctx, ctxKeySetupDone, setupDone.Load())

			cookie, err := r.Cookie(SessionCookieName)
			if err == nil && cookie.Value != "" {
				tokenHash := auth.HashToken(cookie.Value)
				sess, err := store.GetSessionByTokenHash(ctx, tokenHash)
				if err == nil && sess != nil && time.Now().Before(sess.ExpiresAt) {
					user, err := store.GetUserByID(ctx, sess.UserID)
					if err == nil && user != nil {
						ctx = context.WithValue(ctx, ctxKeyUser, user)
						ctx = context.WithValue(ctx, ctxKeySession, sess)
						ctx = withActiveHousehold(ctx, store, user, sess, multiTenant)
					}
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// withActiveHousehold resolves which household this request acts on: the
// session's chosen one if the user may still see it, else their first
// membership. Instance admins may see every household (as owner) and, with
// no memberships of their own, land in the oldest one.
//
// With multiTenant off there is only the oldest household: memberships of
// any other (left over from a multi-tenant install) are ignored, the
// session's choice is overridden, and a user who is not a member of it gets
// no household at all - the same as a multi-tenant user nobody has added yet.
func withActiveHousehold(ctx context.Context, store db.Store, user *db.User, sess *db.Session, multiTenant bool) context.Context {
	memberships, err := store.ListMembershipsForUser(ctx, user.ID)
	if err != nil {
		log.Printf("middleware: memberships for user %d: %v", user.ID, err)
		return ctx
	}
	activeID := sess.ActiveHouseholdID
	if !multiTenant {
		all, err := store.ListHouseholds(ctx)
		if err != nil || len(all) == 0 {
			return ctx
		}
		activeID = all[0].ID
		var only []*db.HouseholdMembership
		for _, m := range memberships {
			if m.HouseholdID == activeID {
				only = append(only, m)
			}
		}
		memberships = only
	}
	ctx = context.WithValue(ctx, ctxKeyMemberships, memberships)

	var hhID int64
	var role string
	for _, m := range memberships {
		if m.HouseholdID == activeID {
			hhID, role = m.HouseholdID, m.Role
		}
	}
	if hhID == 0 && user.IsAdmin() && activeID != 0 {
		hhID = activeID
	}
	if hhID == 0 && len(memberships) > 0 {
		hhID, role = memberships[0].HouseholdID, memberships[0].Role
	}
	if hhID == 0 && user.IsAdmin() {
		if all, err := store.ListHouseholds(ctx); err == nil && len(all) > 0 {
			hhID = all[0].ID
		}
	}
	if hhID == 0 {
		return ctx
	}
	if user.IsAdmin() {
		role = db.HouseholdRoleOwner
	}

	hh, err := store.GetHousehold(ctx, hhID)
	if err != nil || hh == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, ctxKeyHousehold, hh)
	ctx = llm.WithHousehold(ctx, hh.ID) // bill this request's LLM calls (ai_runs)
	return context.WithValue(ctx, ctxKeyRole, role)
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

// ── Role gates ───────────────────────────────────────────────────────────────

// RequireHouseholdRole lets a request through only when the user's role in the
// active household is at least min. A signed-in user with no household at all
// is sent to /households to create or be added to one.
//
// Independently of min, a viewer may only use safe methods: GET, HEAD and
// OPTIONS. That is an allowlist, not a blocklist of POST/PUT/DELETE, so a
// method added later can't slip past (QSS security design §8.3). It relies on
// GET never changing state.
func RequireHouseholdRole(min string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if HouseholdFromCtx(r) == nil {
				http.Redirect(w, r, "/households", http.StatusSeeOther)
				return
			}
			role := HouseholdRoleFromCtx(r)
			if db.HouseholdRoleRank(role) < db.HouseholdRoleRank(min) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			if role == db.HouseholdRoleViewer {
				switch r.Method {
				case http.MethodGet, http.MethodHead, http.MethodOptions:
				default:
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireInstanceAdmin gates server-wide surfaces: settings, AI keys,
// scraper tooling, AI pricing references, logs and account management.
func RequireInstanceAdmin(next http.Handler) http.Handler {
	return RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !UserFromCtx(r).IsAdmin() {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
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
