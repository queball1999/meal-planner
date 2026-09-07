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

	// Health check - used by reverse proxies; exempt from auth and CSRF.
	mux.HandleFunc("GET /health", s.handleHealth)

	// Auth flows
	mux.HandleFunc("GET /auth/login", s.handleLoginPage)
	mux.HandleFunc("POST /auth/login", s.handleLogin)

	// Setup wizard (accessible only when no household exists)
	mux.HandleFunc("GET /setup", s.handleSetupPage)
	mux.HandleFunc("GET /api/zip-state", s.handleZIPState) // public: the setup wizard needs it before login
	mux.HandleFunc("POST /setup", s.handleSetup)

	// ── Auth-required ──────────────────────────────────────────────────────

	requireAuth := middleware.RequireAuth

	mux.Handle("POST /auth/logout", requireAuth(http.HandlerFunc(s.handleLogout)))

	mux.Handle("GET /preferences", requireAuth(http.HandlerFunc(s.handlePreferencesPage)))
	mux.Handle("POST /preferences", requireAuth(http.HandlerFunc(s.handlePreferences)))
	mux.Handle("POST /preferences/members", requireAuth(http.HandlerFunc(s.handleMemberCreate)))
	mux.Handle("POST /preferences/members/{id}", requireAuth(http.HandlerFunc(s.handleMemberUpdate)))
	mux.Handle("POST /preferences/members/{id}/delete", requireAuth(http.HandlerFunc(s.handleMemberDelete)))

	mux.Handle("GET /stores", requireAuth(http.HandlerFunc(s.handleStoresPage)))
	mux.Handle("POST /stores", requireAuth(http.HandlerFunc(s.handleStoreCreate)))
	mux.Handle("POST /stores/select", requireAuth(http.HandlerFunc(s.handleStoreSelect)))
	mux.Handle("POST /stores/{id}/delete", requireAuth(http.HandlerFunc(s.handleStoreDelete)))

	mux.Handle("GET /list", requireAuth(http.HandlerFunc(s.handleShoppingListRedirect)))
	mux.Handle("GET /list/shop", requireAuth(http.HandlerFunc(s.handleInStore)))
	mux.Handle("GET /list/export", requireAuth(http.HandlerFunc(s.handleShoppingListExport)))
	mux.Handle("POST /list/sync", requireAuth(http.HandlerFunc(s.handleListSync)))
	mux.Handle("GET /list/sync/status", requireAuth(http.HandlerFunc(s.handleListSyncStatus)))
	mux.Handle("POST /list/{id}/check", requireAuth(http.HandlerFunc(s.handleShoppingListCheck)))
	mux.Handle("POST /list/{id}/have", requireAuth(http.HandlerFunc(s.handleShoppingListHave)))
	mux.Handle("GET /list/{id}/match", requireAuth(http.HandlerFunc(s.handleItemMatchOptions)))
	mux.Handle("POST /list/{id}/match", requireAuth(http.HandlerFunc(s.handleItemMatch)))
	mux.Handle("GET /list/{id}/price", requireAuth(http.HandlerFunc(s.handleShoppingItemPriceGet)))
	mux.Handle("POST /list/{id}/price", requireAuth(http.HandlerFunc(s.handleShoppingItemPriceSet)))
	mux.Handle("POST /list/ai-cost", requireAuth(http.HandlerFunc(s.handleShoppingAICostAnalysis)))

	mux.Handle("GET /plan", requireAuth(http.HandlerFunc(s.handlePlanPage)))
	mux.Handle("GET /plan/list", requireAuth(http.HandlerFunc(s.handlePlanPage)))
	mux.Handle("GET /plan/history", requireAuth(http.HandlerFunc(s.handlePlanHistory)))
	mux.Handle("POST /plan/generate", requireAuth(http.HandlerFunc(s.handlePlanGenerate)))
	mux.Handle("GET /plan/generate", requireAuth(http.HandlerFunc(s.handlePlanGeneratePage)))
	mux.Handle("GET /plan/generate/status", requireAuth(http.HandlerFunc(s.handlePlanGenerateStatus)))
	mux.Handle("POST /plan/days/{date}/headcount", requireAuth(http.HandlerFunc(s.handlePlanHeadcount)))
	mux.Handle("GET /plan/days/{date}/status-impact", requireAuth(http.HandlerFunc(s.handleDayStatusImpact)))
	mux.Handle("POST /plan/days/{date}/status", requireAuth(http.HandlerFunc(s.handleDayStatus)))
	mux.Handle("GET /chat/history", requireAuth(http.HandlerFunc(s.handleChatHistory)))
	mux.Handle("POST /chat/send", requireAuth(http.HandlerFunc(s.handleChatSend)))
	mux.Handle("POST /chat/clear", requireAuth(http.HandlerFunc(s.handleChatClear)))

	mux.Handle("GET /plan/recipe-options", requireAuth(http.HandlerFunc(s.handleRecipeOptions)))
	mux.Handle("POST /plan/days/{date}/{slot}/fill", requireAuth(http.HandlerFunc(s.handleMealFill)))
	mux.Handle("POST /plan/{id}/delete", requireAuth(http.HandlerFunc(s.handlePlanDelete)))

	mux.Handle("GET /admin/prices", requireAuth(http.HandlerFunc(s.handleAdminPricesPage)))
	mux.Handle("POST /admin/prices", requireAuth(http.HandlerFunc(s.handleAdminPriceCreate)))
	mux.Handle("POST /admin/prices/{id}/delete", requireAuth(http.HandlerFunc(s.handleAdminPriceDelete)))

	mux.Handle("GET /pantry", requireAuth(http.HandlerFunc(s.handlePantryPage)))
	mux.Handle("POST /pantry", requireAuth(http.HandlerFunc(s.handlePantryAdd)))

	// Items catalog - literal routes before /pantry/{id} wildcards (§8.2)
	mux.Handle("GET /pantry/items", requireAuth(http.HandlerFunc(s.handleItemsPage)))
	mux.Handle("POST /pantry/items", requireAuth(http.HandlerFunc(s.handleItemCreate)))
	mux.Handle("GET /pantry/items/{id}", requireAuth(http.HandlerFunc(s.handleItemDetail)))
	mux.Handle("POST /pantry/items/{id}/edit", requireAuth(http.HandlerFunc(s.handleItemEdit)))
	mux.Handle("POST /pantry/items/{id}/delete", requireAuth(http.HandlerFunc(s.handleItemDelete)))
	mux.Handle("POST /pantry/items/{id}/quick-add", requireAuth(http.HandlerFunc(s.handleItemQuickAdd)))
	mux.Handle("POST /pantry/items/{id}/image", requireAuth(http.HandlerFunc(s.handleItemImageReplace)))
	mux.Handle("POST /pantry/items/{id}/packages", requireAuth(http.HandlerFunc(s.handleItemPackageUpsert)))
	mux.Handle("POST /pantry/items/{id}/packages/{pkgID}/delete", requireAuth(http.HandlerFunc(s.handleItemPackageDelete)))
	mux.Handle("POST /pantry/items/{id}/conversions", requireAuth(http.HandlerFunc(s.handleItemConversionUpsert)))
	mux.Handle("POST /pantry/items/{id}/conversions/{cid}/delete", requireAuth(http.HandlerFunc(s.handleItemConversionDelete)))

	mux.Handle("POST /pantry/{id}/delete", requireAuth(http.HandlerFunc(s.handlePantryDelete)))
	mux.Handle("POST /pantry/{id}/stock", requireAuth(http.HandlerFunc(s.handlePantryStock)))

	mux.Handle("GET /meals/{id}", requireAuth(http.HandlerFunc(s.handleMealDetail)))
	mux.Handle("GET /meals/{id}/card", requireAuth(http.HandlerFunc(s.handleMealCard)))
	mux.Handle("POST /meals/{id}/feedback", requireAuth(http.HandlerFunc(s.handleMealFeedback)))
	mux.Handle("POST /meals/{id}/lock", requireAuth(http.HandlerFunc(s.handleMealLock)))

	// Recipe catalog - literal routes before /recipes/{id} wildcard (§8.2)
	mux.Handle("GET /recipes/import", requireAuth(http.HandlerFunc(s.handleRecipeImportPage)))
	mux.Handle("POST /recipes/import", requireAuth(http.HandlerFunc(s.handleRecipeImport)))
	mux.Handle("POST /recipes/import/manual", requireAuth(http.HandlerFunc(s.handleRecipeImportManual)))
	mux.Handle("GET /recipes", requireAuth(http.HandlerFunc(s.handleRecipesPage)))
	mux.Handle("GET /recipes/{id}", requireAuth(http.HandlerFunc(s.handleRecipeDetail)))
	mux.Handle("POST /recipes/{id}/edit", requireAuth(http.HandlerFunc(s.handleRecipeEdit)))
	mux.Handle("POST /recipes/{id}/reimport", requireAuth(http.HandlerFunc(s.handleRecipeReimport)))
	mux.Handle("POST /recipes/{id}/image", requireAuth(http.HandlerFunc(s.handleRecipeImageReplace)))
	mux.Handle("POST /recipes/{id}/delete", requireAuth(http.HandlerFunc(s.handleRecipeDelete)))
	mux.Handle("GET /recipe-images/{name}", requireAuth(http.HandlerFunc(s.handleRecipeImageServe)))
	mux.Handle("GET /item-images/{name}", requireAuth(http.HandlerFunc(s.handleItemImageServe)))

	mux.Handle("GET /admin/scrape", requireAuth(http.HandlerFunc(s.handleScrapeConfigPage)))
	mux.Handle("POST /admin/scrape/{storeID}/save", requireAuth(http.HandlerFunc(s.handleScrapeConfigSave)))
	mux.Handle("POST /admin/scrape/{storeID}/test", requireAuth(http.HandlerFunc(s.handleScrapeConfigTest)))
	mux.Handle("GET /admin/scrape/proxy", requireAuth(http.HandlerFunc(s.handleScrapeProxy)))
	mux.Handle("POST /admin/scrape/fetch", requireAuth(http.HandlerFunc(s.handleScrapeFetch)))
	mux.Handle("POST /admin/scrape/selector", requireAuth(http.HandlerFunc(s.handleScrapeSelector)))
	mux.Handle("POST /admin/scrape/ai-extract", requireAuth(http.HandlerFunc(s.handleScrapeAIExtract)))
	mux.Handle("POST /admin/scrape/autodetect", requireAuth(http.HandlerFunc(s.handleScrapeAutodetect)))

	mux.Handle("GET /settings", requireAuth(http.HandlerFunc(s.handleSettingsPage)))
	mux.Handle("POST /settings/danger/{target}", requireAuth(http.HandlerFunc(s.handleDangerWipe)))
	mux.Handle("GET /about", requireAuth(http.HandlerFunc(s.handleAbout)))
	mux.Handle("GET /about/probe", requireAuth(http.HandlerFunc(s.handleAboutProbe)))
	mux.Handle("POST /settings/save", requireAuth(http.HandlerFunc(s.handleSettingsSave)))
	mux.Handle("POST /settings/test-render", requireAuth(http.HandlerFunc(s.handleSettingsTestRender)))
	mux.Handle("POST /settings/test-ai", requireAuth(http.HandlerFunc(s.handleSettingsTestAI)))
	mux.Handle("POST /settings/ha/test", requireAuth(http.HandlerFunc(s.handleHATest)))
	mux.Handle("POST /settings/ha/entities", requireAuth(http.HandlerFunc(s.handleHAEntities)))
	mux.Handle("POST /settings/ha/save", requireAuth(http.HandlerFunc(s.handleHASave)))
	// TODO: mux.Handle("POST /settings/ai/models", requireAuth(http.HandlerFunc(s.handleAIModels)))
	// handleAIModels doesn't exist yet and nothing in the UI calls this route -
	// commented out so the build isn't broken; wire it up when that handler lands.
	mux.Handle("GET /admin/llm-debug", requireAuth(http.HandlerFunc(s.handleLLMDebugLog)))
	mux.Handle("GET /admin/llm-log", requireAuth(http.HandlerFunc(s.handleLLMLogPage)))
	mux.Handle("GET /search", requireAuth(http.HandlerFunc(s.handleSearch)))
	mux.Handle("GET /attributions", requireAuth(http.HandlerFunc(s.handleAttributionsPage)))

	mux.Handle("GET /scan", requireAuth(http.HandlerFunc(s.handleScanPage)))
	mux.Handle("GET /scan/{code}", requireAuth(http.HandlerFunc(s.handleScanCode)))
	mux.Handle("POST /pantry/scan", requireAuth(http.HandlerFunc(s.handlePantryScan)))

	mux.Handle("GET /", requireAuth(http.HandlerFunc(s.handleDashboard)))
}
