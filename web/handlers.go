package web

import (
	"encoding/json"
	"net/http"
)

// handleHealth returns a JSON health check. Used by reverse proxies and
// uptime monitors; not behind auth.
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

// handleDashboard serves the main dashboard (Phase 0: skeleton placeholder).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "index", nil)
}
