package web

import (
	"net/http"
	"net/url"
	"time"

	"goeat/db"
	"goeat/middleware"
)

const flashCookieName = "goeat_flash"

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

// ── Flash messages ──────────────────────────────────────────────────────────

// setFlash stores a one-shot message in a short-lived cookie. The message is
// read and cleared by popFlash on the next render call.
func (s *Server) setFlash(w http.ResponseWriter, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    url.QueryEscape(msg),
		Path:     "/",
		MaxAge:   30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// popFlash reads and clears the flash cookie, returning its value (or "").
func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookieName)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	msg, _ := url.QueryUnescape(c.Value)
	return msg
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
