package web

import (
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
