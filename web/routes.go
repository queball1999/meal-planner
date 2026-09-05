package web

import "net/http"

// routes registers all HTTP handlers. Literal routes must appear before
// wildcard routes so they win (e.g. /recipes/import before /recipes/{id}).
func (s *Server) routes() {
	// Static assets — served from the embedded FS; path includes "static/" prefix.
	s.mux.Handle("GET /static/", http.FileServerFS(staticFS))

	// Health check — used by reverse proxies and uptime monitors.
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Application pages (Phase 0: placeholders only).
	s.mux.HandleFunc("GET /", s.handleDashboard)
}
