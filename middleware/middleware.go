// Package middleware provides HTTP middleware: session loading, auth gates,
// and IP extraction. Context keys are defined here so both middleware and web
// handlers share them without a circular import.
package middleware

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"goeat/auth"
	"goeat/db"
	"goeat/llm"
)

// SessionCookieName is the name of the session cookie, shared with the web
// package so both read and write the same cookie. On the https origin it
// becomes SecureSessionCookieName instead (see SessionOptions.CookieFor).
const SessionCookieName = "goeat_session"

// SecureSessionCookieName carries the __Host- prefix: the browser only
// accepts it with Secure, Path=/ and no Domain, so a sibling subdomain can't
// set or overwrite it (QSS security design §1.1).
const SecureSessionCookieName = "__Host-goeat_session"

// SessionOptions configures LoadSession.
type SessionOptions struct {
	// MultiTenant is ENABLE_MULTI_TENANT - see withActiveHousehold.
	MultiTenant bool
	// IdleTimeout signs a session out after this long without activity
	// (SESSION_IDLE_MINUTES). 0 = no idle limit.
	IdleTimeout time.Duration
	// SecureHost is PUBLIC_BASE_URL's host when that URL is https. Requests
	// for that host get the Secure __Host- cookie; any other host (a LAN
	// address over plain http) keeps the plain one, which a browser would
	// refuse to store with Secure set.
	SecureHost string
}

// CookieFor returns the session cookie name for r and whether it is Secure.
func (o SessionOptions) CookieFor(r *http.Request) (name string, secure bool) {
	if o.SecureHost != "" && strings.EqualFold(r.Host, o.SecureHost) {
		return SecureSessionCookieName, true
	}
	return SessionCookieName, false
}

// touchEvery bounds last_seen_at writes to one a minute per session.
const touchEvery = time.Minute

// isActivity reports whether r is something the user did - a page load or a
// form post - rather than background traffic (SSE streams, status polls,
// fragment refreshes). Only activity refreshes last_seen_at, or an open tab
// would keep the session alive forever (QSS security design §1.4).
func isActivity(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	// Browsers too old to send Sec-Fetch-*: a request for a page counts.
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

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
// With MultiTenant off the instance has one household (the oldest) and
// every request acts on it - see withActiveHousehold.
//
// A session is refused once it passes its fixed expiry or has been idle for
// longer than opts.IdleTimeout; an idle one is deleted on the spot.
func LoadSession(store db.Store, opts SessionOptions) func(http.Handler) http.Handler {
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

			name, _ := opts.CookieFor(r)
			cookie, err := r.Cookie(name)
			if err == nil && cookie.Value != "" {
				tokenHash := auth.HashToken(cookie.Value)
				sess, err := store.GetSessionByTokenHash(ctx, tokenHash)
				now := time.Now()
				if err == nil && sess != nil && opts.IdleTimeout > 0 && now.Sub(sess.LastSeenAt) > opts.IdleTimeout {
					_ = store.DeleteSession(ctx, sess.ID)
					sess = nil
				}
				if err == nil && sess != nil && now.Before(sess.ExpiresAt) {
					user, err := store.GetUserByID(ctx, sess.UserID)
					if err == nil && user != nil {
						if isActivity(r) && now.Sub(sess.LastSeenAt) >= touchEvery {
							if err := store.TouchSession(ctx, sess.ID, now); err == nil {
								sess.LastSeenAt = now
							}
						}
						ctx = context.WithValue(ctx, ctxKeyUser, user)
						ctx = context.WithValue(ctx, ctxKeySession, sess)
						ctx = withActiveHousehold(ctx, store, user, sess, opts.MultiTenant)
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

// RealIP rewrites r.RemoteAddr to the real client address, believing
// X-Forwarded-For only when the direct peer is one of trusted (the reverse
// proxies in TRUSTED_PROXIES). It then walks the header right to left and
// takes the first address that isn't itself a trusted proxy - the leftmost
// entry is whatever the client chose to write (QSS security design §5.4).
// With no trusted proxies the header is ignored entirely.
//
// Wrap the outermost layer so rate limiting, lockout and the audit log all
// see the same address.
func RealIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(trusted) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip, ok := forwardedClient(r, trusted); ok {
				r2 := r.Clone(r.Context())
				r2.RemoteAddr = net.JoinHostPort(ip.String(), "0")
				r = r2
			}
			next.ServeHTTP(w, r)
		})
	}
}

// forwardedClient returns the client address from X-Forwarded-For when the
// peer is a trusted proxy, or ok=false to keep the peer address.
func forwardedClient(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok || !inPrefixes(peer, trusted) {
		return netip.Addr{}, false
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	var last netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // garbage: stop at the last address we could trust
		}
		addr = addr.Unmap()
		if !inPrefixes(addr, trusted) {
			return addr, true
		}
		last = addr
	}
	if last.IsValid() {
		return last, true // every hop was a proxy
	}
	return netip.Addr{}, false
}

func parseAddr(hostport string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func inPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientIP returns the client's IP: the direct peer address, which RealIP
// has already replaced with the forwarded client when a trusted proxy sent
// the request. Request headers are never read here.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return r.RemoteAddr
	}
	return host
}
