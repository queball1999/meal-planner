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
	mux.Handle("GET /plan/history", requireAuth(http.HandlerFunc(s.handlePlanHistory)))
	mux.Handle("POST /plan/generate", requireAuth(http.HandlerFunc(s.handlePlanGenerate)))
	mux.Handle("GET /plan/generate", requireAuth(http.HandlerFunc(s.handlePlanGeneratePage)))
	mux.Handle("GET /plan/generate/status", requireAuth(http.HandlerFunc(s.handlePlanGenerateStatus)))
	mux.Handle("POST /plan/days/{date}/headcount", requireAuth(http.HandlerFunc(s.handlePlanHeadcount)))

	mux.Handle("GET /admin/prices", requireAuth(http.HandlerFunc(s.handleAdminPricesPage)))
	mux.Handle("POST /admin/prices", requireAuth(http.HandlerFunc(s.handleAdminPriceCreate)))
	mux.Handle("POST /admin/prices/{id}/delete", requireAuth(http.HandlerFunc(s.handleAdminPriceDelete)))

	mux.Handle("GET /pantry", requireAuth(http.HandlerFunc(s.handlePantryPage)))
	mux.Handle("POST /pantry", requireAuth(http.HandlerFunc(s.handlePantryAdd)))
	mux.Handle("POST /pantry/{id}/delete", requireAuth(http.HandlerFunc(s.handlePantryDelete)))
	mux.Handle("POST /pantry/{id}/stock", requireAuth(http.HandlerFunc(s.handlePantryStock)))

	mux.Handle("GET /meals/{id}", requireAuth(http.HandlerFunc(s.handleMealDetail)))
	mux.Handle("POST /meals/{id}/feedback", requireAuth(http.HandlerFunc(s.handleMealFeedback)))
	mux.Handle("POST /meals/{id}/lock", requireAuth(http.HandlerFunc(s.handleMealLock)))

	// Recipe catalog — literal routes before /recipes/{id} wildcard (§8.2)
	mux.Handle("GET /recipes/import", requireAuth(http.HandlerFunc(s.handleRecipeImportPage)))
	mux.Handle("POST /recipes/import", requireAuth(http.HandlerFunc(s.handleRecipeImport)))
	mux.Handle("POST /recipes/import/manual", requireAuth(http.HandlerFunc(s.handleRecipeImportManual)))
	mux.Handle("GET /recipes", requireAuth(http.HandlerFunc(s.handleRecipesPage)))
	mux.Handle("GET /recipes/{id}", requireAuth(http.HandlerFunc(s.handleRecipeDetail)))
	mux.Handle("POST /recipes/{id}/delete", requireAuth(http.HandlerFunc(s.handleRecipeDelete)))
	mux.Handle("GET /recipe-images/{name}", requireAuth(http.HandlerFunc(s.handleRecipeImageServe)))

	mux.Handle("GET /admin/scrape", requireAuth(http.HandlerFunc(s.handleScrapeConfigPage)))
	mux.Handle("POST /admin/scrape/{storeID}/save", requireAuth(http.HandlerFunc(s.handleScrapeConfigSave)))
	mux.Handle("POST /admin/scrape/{storeID}/test", requireAuth(http.HandlerFunc(s.handleScrapeConfigTest)))
	mux.Handle("GET /admin/scrape/proxy", requireAuth(http.HandlerFunc(s.handleScrapeProxy)))
	mux.Handle("POST /admin/scrape/fetch", requireAuth(http.HandlerFunc(s.handleScrapeFetch)))
	mux.Handle("POST /admin/scrape/selector", requireAuth(http.HandlerFunc(s.handleScrapeSelector)))
	mux.Handle("POST /admin/scrape/autodetect", requireAuth(http.HandlerFunc(s.handleScrapeAutodetect)))

	mux.Handle("GET /settings", requireAuth(http.HandlerFunc(s.handleSettingsPage)))
	mux.Handle("POST /settings/test-ai", requireAuth(http.HandlerFunc(s.handleSettingsTestAI)))
	mux.Handle("GET /admin/llm-debug", requireAuth(http.HandlerFunc(s.handleLLMDebugLog)))
	mux.Handle("GET /search", requireAuth(http.HandlerFunc(s.handleSearch)))

	mux.Handle("GET /scan", requireAuth(http.HandlerFunc(s.handleScanPage)))
	mux.Handle("GET /scan/{code}", requireAuth(http.HandlerFunc(s.handleScanCode)))
	mux.Handle("POST /pantry/scan", requireAuth(http.HandlerFunc(s.handlePantryScan)))

	mux.Handle("GET /", requireAuth(http.HandlerFunc(s.handleDashboard)))
}
