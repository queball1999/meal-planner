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

	mux.Handle("GET /preferences", requireAuth(http.HandlerFunc(s.handlePreferencesPage)))
	mux.Handle("POST /preferences", requireAuth(http.HandlerFunc(s.handlePreferences)))

	mux.Handle("GET /stores", requireAuth(http.HandlerFunc(s.handleStoresPage)))
	mux.Handle("POST /stores", requireAuth(http.HandlerFunc(s.handleStoreCreate)))
	mux.Handle("POST /stores/{id}/delete", requireAuth(http.HandlerFunc(s.handleStoreDelete)))

	mux.Handle("GET /list", requireAuth(http.HandlerFunc(s.handleShoppingListPage)))
	mux.Handle("POST /list/{id}/check", requireAuth(http.HandlerFunc(s.handleShoppingListCheck)))

	mux.Handle("GET /plan", requireAuth(http.HandlerFunc(s.handlePlanPage)))
	mux.Handle("POST /plan/generate", requireAuth(http.HandlerFunc(s.handlePlanGenerate)))
	mux.Handle("GET /plan/generate", requireAuth(http.HandlerFunc(s.handlePlanGeneratePage)))
	mux.Handle("GET /plan/generate/status", requireAuth(http.HandlerFunc(s.handlePlanGenerateStatus)))

	mux.Handle("GET /", requireAuth(http.HandlerFunc(s.handleDashboard)))
}
