package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"goeat/auth"
	"goeat/middleware"
)

// handleLoginPage renders the sign-in form. Redirects away when already
// signed in or when setup has not been completed.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if middleware.UserFromCtx(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if middleware.HouseholdFromCtx(r) == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", nil)
}

// handleLogin processes the sign-in form (POST /auth/login).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Invalid form submission")
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		s.setNotify(w, NotifyDanger, "Username and password are required")
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	ctx := r.Context()

	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil || user == nil {
		s.logEvent(r, nil, "auth.login.failed", "user", "", `{"reason":"user_not_found"}`)
		// Deliberate constant-time response: do a dummy bcrypt compare so
		// timing doesn't reveal whether the username exists.
		_ = auth.CheckPassword("$2a$12$notavalidhashXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", password)
		s.setNotify(w, NotifyDanger, "Invalid username or password")
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, password); err != nil {
		id := user.ID
		s.logEvent(r, &id, "auth.login.failed", "user", fmt.Sprintf("%d", id), `{"reason":"wrong_password"}`)
		s.setNotify(w, NotifyDanger, "Invalid username or password")
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(time.Duration(s.cfg.SessionTTLHours) * time.Hour)
	_, err = s.store.CreateSession(ctx, user.ID, auth.HashToken(token),
		middleware.ClientIP(r), r.UserAgent(), expiresAt)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.setSessionCookie(w, token, expiresAt)
	id := user.ID
	s.logEvent(r, &id, "auth.login", "user", fmt.Sprintf("%d", id), "")

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout clears the session cookie and deletes the server-side session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromCtx(r)

	cookie, err := r.Cookie(middleware.SessionCookieName)
	if err == nil {
		sess, err := s.store.GetSessionByTokenHash(r.Context(), auth.HashToken(cookie.Value))
		if err == nil && sess != nil {
			_ = s.store.DeleteSession(r.Context(), sess.ID)
		}
	}

	s.clearSessionCookie(w)

	if user != nil {
		id := user.ID
		s.logEvent(r, &id, "auth.logout", "user", fmt.Sprintf("%d", id), "")
	}

	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}
