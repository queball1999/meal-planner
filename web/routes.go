package web

import (
	"net/http"

	"goeat/middleware"
)

// routes registers all HTTP handlers on mux. Literal routes must appear before
// wildcard routes so they win (§8.2 note on /recipes/import vs /recipes/{id}).
func (s *Server) routes(mux *http.ServeMux) {
	// ── Public (no auth) ───────────────────────────────────────────────────

	// Static assets from the embedded FS; path includes the "static/" prefix.
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	// Health check — used by reverse proxies; exempt from auth and CSRF.
	mux.HandleFunc("GET /health", s.handleHealth)

	// Auth flows
	mux.HandleFunc("GET /auth/login", s.handleLoginPage)
	mux.HandleFunc("POST /auth/login", s.handleLogin)

	// Setup wizard (accessible only when no household exists)
	mux.HandleFunc("GET /setup", s.handleSetupPage)
	mux.HandleFunc("POST /setup", s.handleSetup)

	// ── Auth-required ──────────────────────────────────────────────────────

	requireAuth := middleware.RequireAuth

	mux.Handle("POST /auth/logout", requireAuth(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /", requireAuth(http.HandlerFunc(s.handleDashboard)))
}
