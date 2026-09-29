package web

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"goeat/db"
	"goeat/middleware"
)

const notifyCookieName = "goeat_notify"

// Notify kinds map to the toast types in main.js ("danger" -> "error").
const (
	NotifySuccess = "success"
	NotifyInfo    = "info"
	NotifyWarning = "warning"
	NotifyDanger  = "danger"
)

// ── Session cookies ──────────────────────────────────────────────────────────

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ── Notify messages ──────────────────────────────────────────────────────────

// setNotify stores a one-shot message and its kind (one of the Notify*
// constants) in a short-lived cookie, surfaced as a toast (main.js) on the
// next page render. Read and cleared by popNotify.
func (s *Server) setNotify(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name:     notifyCookieName,
		Value:    kind + "|" + url.QueryEscape(msg),
		Path:     "/",
		MaxAge:   30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// popNotify reads and clears the notify cookie, returning its kind and
// message (both "" when there is none).
func (s *Server) popNotify(w http.ResponseWriter, r *http.Request) (kind, msg string) {
	c, err := r.Cookie(notifyCookieName)
	if err != nil {
		return "", ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     notifyCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	kind, escaped, ok := strings.Cut(c.Value, "|")
	if !ok {
		// Pre-existing cookie from before notify kinds existed.
		msg, _ = url.QueryUnescape(c.Value)
		return NotifyInfo, msg
	}
	msg, _ = url.QueryUnescape(escaped)
	return kind, msg
}

// ── Audit log helper ─────────────────────────────────────────────────────────

// logEvent fires an audit-log entry. Errors are swallowed (fire-and-forget).
func (s *Server) logEvent(r *http.Request, userID *int64, action, targetType, targetID, metadata string) {
	label := ""
	if u := middleware.UserFromCtx(r); u != nil {
		label = u.Username
	}
	_ = s.store.LogEvent(r.Context(), db.AppEvent{
		ActorUserID: userID,
		ActorLabel:  label,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    metadata,
		IPAddress:   middleware.ClientIP(r),
		UserAgent:   r.UserAgent(),
		Status:      "ok",
	})
}

// serveHouseholdImage serves dir/name when one of the active household's
// recipes or items references that file, and 404s otherwise - so a guessed
// or leaked file name from another household serves nothing (QSS §17).
func (s *Server) serveHouseholdImage(w http.ResponseWriter, r *http.Request, dir string, kind db.ImageKind) {
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.NotFound(w, r)
		return
	}
	used, err := s.store.HouseholdUsesImage(r.Context(), hh.ID, kind, name)
	if err != nil {
		log.Printf("serve image %s/%s: %v", kind, name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !used {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, dir+"/"+name)
}

// ── Tenancy ──────────────────────────────────────────────────────────────────

// owns reports whether the row (kind, id) belongs to the active household,
// answering 404 itself when it doesn't - a caller just returns on false.
// 404 rather than 403 so probing ids can't confirm another household's rows
// exist. Every handler that takes an id from the path or form must call this
// (or check the loaded row's HouseholdID) before reading or writing the row.
func (s *Server) owns(w http.ResponseWriter, r *http.Request, kind db.ResourceKind, id int64) bool {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.NotFound(w, r)
		return false
	}
	ok, err := s.store.HouseholdOwns(r.Context(), hh.ID, kind, id)
	if err != nil {
		log.Printf("owns %s %d: %v", kind, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.NotFound(w, r)
		return false
	}
	return true
}
