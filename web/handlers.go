package web

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/csrf"

	"goeat/middleware"
)

// handleHealth returns a JSON health check. Not behind auth; used by reverse
// proxies and uptime monitors.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK

	if err := s.store.Ping(r.Context()); err != nil {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  status,
		"version": s.version,
	})
}

// handleDashboard serves the main dashboard. RequireAuth middleware guarantees
// a user is in context; we additionally guard against the household not yet
// existing (setup not completed).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if middleware.HouseholdFromCtx(r) == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, "index", nil)
}

// handleCSRFError is the gorilla/csrf error handler — returns a plain 403 with
// the failure reason so clients get an actionable message.
func (s *Server) handleCSRFError(w http.ResponseWriter, r *http.Request) {
	reason := csrf.FailureReason(r)
	http.Error(w, "CSRF check failed: "+reason.Error(), http.StatusForbidden)
}
