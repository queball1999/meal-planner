package web

import (
	"net/http"

	"goeat/db"
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

	// Store logos: public because the setup wizard shows them before login;
	// only catalog domains are ever fetched (web/store_logos.go).
	mux.HandleFunc("GET /store-logos/{domain}", s.handleStoreLogo)

	// ── Auth-required ──────────────────────────────────────────────────────

	// Every route below names the least it needs (phase 13):
	//   requireAuth - any signed-in user, no household needed (own account)
	//   view/edit/own - that role or better in the active household; a
	//                   viewer is additionally held to GET/HEAD/OPTIONS
	//   admin       - instance admin: server settings, AI keys, scraper, logs
	// A handler that takes an id from the request must still check the row
	// belongs to the active household (s.owns) - the gate says who may act,
	// not on what.
	requireAuth := middleware.RequireAuth
	view := middleware.RequireHouseholdRole(db.HouseholdRoleViewer)
	edit := middleware.RequireHouseholdRole(db.HouseholdRoleEditor)
	own := middleware.RequireHouseholdRole(db.HouseholdRoleOwner)
	admin := middleware.RequireInstanceAdmin

	mux.Handle("POST /auth/logout", requireAuth(http.HandlerFunc(s.handleLogout)))

	// Self-service account page: password, data export, and the destructive
	// resets. Literal routes only.
	mux.Handle("GET /account", requireAuth(http.HandlerFunc(s.handleAccountPage)))
	mux.Handle("POST /account/password", requireAuth(http.HandlerFunc(s.handleAccountPassword)))
	mux.Handle("GET /account/export", view(http.HandlerFunc(s.handleAccountExport)))
	mux.Handle("POST /account/reset", own(http.HandlerFunc(s.handleAccountReset)))
	mux.Handle("POST /account/wipe", own(http.HandlerFunc(s.handleAccountWipe)))

	// Households: switching and the people page need only a login (a user
	// with no household yet lands here); managing people needs owner;
	// creating/deleting households and accounts is the server admin's job.
	mux.Handle("GET /households", requireAuth(http.HandlerFunc(s.handleHouseholdsPage)))
	// Switching, creating and deleting households only exist with
	// ENABLE_MULTI_TENANT on; off, they 404 (s.multiTenantOnly).
	mux.Handle("POST /households/switch", s.multiTenantOnly(requireAuth(http.HandlerFunc(s.handleHouseholdSwitch))))
	mux.Handle("POST /households", s.multiTenantOnly(admin(http.HandlerFunc(s.handleHouseholdCreate))))
	mux.Handle("POST /households/rename", own(http.HandlerFunc(s.handleHouseholdRename)))
	mux.Handle("POST /households/delete", s.multiTenantOnly(admin(http.HandlerFunc(s.handleHouseholdDelete))))
	mux.Handle("POST /households/members", own(http.HandlerFunc(s.handleHouseholdMemberAdd)))
	mux.Handle("POST /households/members/{userID}/role", own(http.HandlerFunc(s.handleHouseholdMemberRole)))
	mux.Handle("POST /households/members/{userID}/remove", own(http.HandlerFunc(s.handleHouseholdMemberRemove)))
	mux.Handle("POST /admin/users/{userID}/role", admin(http.HandlerFunc(s.handleUserRole)))
	mux.Handle("POST /admin/users/{userID}/delete", admin(http.HandlerFunc(s.handleUserDelete)))

	mux.Handle("GET /preferences", view(http.HandlerFunc(s.handlePreferencesPage)))
	mux.Handle("POST /preferences", own(http.HandlerFunc(s.handlePreferences)))
	mux.Handle("POST /preferences/members", own(http.HandlerFunc(s.handleMemberCreate)))
	mux.Handle("POST /preferences/members/{id}", own(http.HandlerFunc(s.handleMemberUpdate)))
	mux.Handle("POST /preferences/members/{id}/delete", own(http.HandlerFunc(s.handleMemberDelete)))

	mux.Handle("GET /stores", view(http.HandlerFunc(s.handleStoresPage)))
	mux.Handle("POST /stores", own(http.HandlerFunc(s.handleStoreCreate)))
	mux.Handle("POST /stores/select", own(http.HandlerFunc(s.handleStoreSelect)))
	mux.Handle("POST /stores/{id}/delete", own(http.HandlerFunc(s.handleStoreDelete)))
	mux.Handle("POST /stores/shares", own(http.HandlerFunc(s.handleStoreShares)))
	mux.Handle("POST /stores/{id}/items", edit(http.HandlerFunc(s.handleStoreItems)))

	mux.Handle("GET /list", view(http.HandlerFunc(s.handleShoppingListRedirect)))
	mux.Handle("GET /list/shop", view(http.HandlerFunc(s.handleInStore)))
	mux.Handle("GET /list/export", view(http.HandlerFunc(s.handleShoppingListExport)))
	mux.Handle("POST /list/sync", edit(http.HandlerFunc(s.handleListSync)))
	mux.Handle("GET /list/sync/status", view(http.HandlerFunc(s.handleListSyncStatus)))
	mux.Handle("POST /list/{id}/check", edit(http.HandlerFunc(s.handleShoppingListCheck)))
	mux.Handle("POST /list/{id}/have", edit(http.HandlerFunc(s.handleShoppingListHave)))
	mux.Handle("GET /list/{id}/match", view(http.HandlerFunc(s.handleItemMatchOptions)))
	mux.Handle("POST /list/{id}/match", edit(http.HandlerFunc(s.handleItemMatch)))
	mux.Handle("GET /list/{id}/price", view(http.HandlerFunc(s.handleShoppingItemPriceGet)))
	mux.Handle("GET /list/{id}/conversions", view(http.HandlerFunc(s.handleShoppingItemConversions)))
	mux.Handle("POST /list/{id}/price", edit(http.HandlerFunc(s.handleShoppingItemPriceSet)))
	mux.Handle("POST /list/ai-cost", edit(http.HandlerFunc(s.handleShoppingAICostAnalysis)))

	mux.Handle("GET /plan", view(http.HandlerFunc(s.handlePlanPage)))
	mux.Handle("GET /plan/list", view(http.HandlerFunc(s.handlePlanPage)))
	mux.Handle("GET /plan/list/fragment", view(http.HandlerFunc(s.handlePlanListFragment)))
	mux.Handle("POST /plan/list/stop-pricing", edit(http.HandlerFunc(s.handlePlanListStopPricing)))
	mux.Handle("GET /plan/history", view(http.HandlerFunc(s.handlePlanHistory)))
	mux.Handle("GET /plan/history/{id}/detail", view(http.HandlerFunc(s.handlePlanHistoryDetail)))
	mux.Handle("POST /plan/generate", edit(http.HandlerFunc(s.handlePlanGenerate)))
	mux.Handle("GET /plan/generate", view(http.HandlerFunc(s.handlePlanGeneratePage)))
	mux.Handle("GET /plan/generate/status", view(http.HandlerFunc(s.handlePlanGenerateStatus)))
	mux.Handle("POST /plan/generate/resume", edit(http.HandlerFunc(s.handlePlanGenerateResume)))
	mux.Handle("POST /plan/days/{date}/headcount", edit(http.HandlerFunc(s.handlePlanHeadcount)))
	mux.Handle("GET /plan/days/{date}/status-impact", view(http.HandlerFunc(s.handleDayStatusImpact)))
	mux.Handle("POST /plan/days/{date}/status", edit(http.HandlerFunc(s.handleDayStatus)))
	mux.Handle("GET /chat/history", view(http.HandlerFunc(s.handleChatHistory)))
	mux.Handle("POST /chat/send", edit(http.HandlerFunc(s.handleChatSend)))
	mux.Handle("POST /chat/confirm", edit(http.HandlerFunc(s.handleChatConfirm)))
	mux.Handle("POST /chat/clear", edit(http.HandlerFunc(s.handleChatClear)))

	mux.Handle("GET /plan/recipe-options", view(http.HandlerFunc(s.handleRecipeOptions)))
	mux.Handle("GET /plan/pantry-options", view(http.HandlerFunc(s.handlePantryOptions)))
	mux.Handle("POST /plan/days/{date}/{slot}/fill", edit(http.HandlerFunc(s.handleMealFill)))
	mux.Handle("POST /plan/{id}/delete", edit(http.HandlerFunc(s.handlePlanDelete)))

	mux.Handle("GET /admin/prices", view(http.HandlerFunc(s.handleAdminPricesPage)))
	mux.Handle("POST /admin/prices", edit(http.HandlerFunc(s.handleAdminPriceCreate)))
	mux.Handle("POST /admin/prices/{id}/delete", edit(http.HandlerFunc(s.handleAdminPriceDelete)))

	mux.Handle("GET /pantry", view(http.HandlerFunc(s.handlePantryPage)))
	mux.Handle("POST /pantry", edit(http.HandlerFunc(s.handlePantryAdd)))

	// Items catalog - literal routes before /pantry/{id} wildcards (§8.2)
	mux.Handle("GET /pantry/items", view(http.HandlerFunc(s.handleItemsPage)))
	mux.Handle("POST /pantry/items", edit(http.HandlerFunc(s.handleItemCreate)))
	mux.Handle("GET /pantry/items/{id}", view(http.HandlerFunc(s.handleItemDetail)))
	mux.Handle("POST /pantry/items/{id}/edit", edit(http.HandlerFunc(s.handleItemEdit)))
	mux.Handle("POST /pantry/items/{id}/delete", edit(http.HandlerFunc(s.handleItemDelete)))
	mux.Handle("POST /pantry/items/{id}/quick-add", edit(http.HandlerFunc(s.handleItemQuickAdd)))
	mux.Handle("POST /pantry/items/{id}/image", edit(http.HandlerFunc(s.handleItemImageReplace)))
	mux.Handle("POST /pantry/items/{id}/packages", edit(http.HandlerFunc(s.handleItemPackageUpsert)))
	mux.Handle("POST /pantry/items/{id}/packages/{pkgID}/delete", edit(http.HandlerFunc(s.handleItemPackageDelete)))
	mux.Handle("POST /pantry/items/{id}/stores/{storeID}/preferred", edit(http.HandlerFunc(s.handleItemStorePreferred)))
	mux.Handle("POST /pantry/items/{id}/conversions", edit(http.HandlerFunc(s.handleItemConversionUpsert)))
	mux.Handle("POST /pantry/items/{id}/conversions/{cid}/delete", edit(http.HandlerFunc(s.handleItemConversionDelete)))

	mux.Handle("POST /pantry/{id}/delete", edit(http.HandlerFunc(s.handlePantryDelete)))
	mux.Handle("POST /pantry/{id}/stock", edit(http.HandlerFunc(s.handlePantryStock)))

	mux.Handle("GET /meals/{id}", view(http.HandlerFunc(s.handleMealDetail)))
	mux.Handle("GET /meals/{id}/card", view(http.HandlerFunc(s.handleMealCard)))
	mux.Handle("POST /meals/{id}/feedback", edit(http.HandlerFunc(s.handleMealFeedback)))
	mux.Handle("POST /meals/{id}/lock", edit(http.HandlerFunc(s.handleMealLock)))
	mux.Handle("GET /meals/{id}/status-impact", view(http.HandlerFunc(s.handleMealStatusImpact)))
	mux.Handle("POST /meals/{id}/status", edit(http.HandlerFunc(s.handleMealStatus)))
	mux.Handle("POST /meals/{id}/swap", edit(http.HandlerFunc(s.handleMealSwap)))

	// Recipe catalog - literal routes before /recipes/{id} wildcard (§8.2)
	mux.Handle("GET /recipes/import", view(http.HandlerFunc(s.handleRecipeImportPage)))
	mux.Handle("POST /recipes/import", edit(http.HandlerFunc(s.handleRecipeImport)))
	mux.Handle("POST /recipes/import/manual", edit(http.HandlerFunc(s.handleRecipeImportManual)))
	mux.Handle("GET /recipes", view(http.HandlerFunc(s.handleRecipesPage)))
	mux.Handle("GET /recipes/{id}", view(http.HandlerFunc(s.handleRecipeDetail)))
	mux.Handle("POST /recipes/{id}/edit", edit(http.HandlerFunc(s.handleRecipeEdit)))
	mux.Handle("POST /recipes/{id}/reimport", edit(http.HandlerFunc(s.handleRecipeReimport)))
	mux.Handle("POST /recipes/{id}/image", edit(http.HandlerFunc(s.handleRecipeImageReplace)))
	mux.Handle("POST /recipes/{id}/delete", edit(http.HandlerFunc(s.handleRecipeDelete)))
	mux.Handle("GET /recipe-images/{name}", view(http.HandlerFunc(s.handleRecipeImageServe)))
	mux.Handle("GET /item-images/{name}", view(http.HandlerFunc(s.handleItemImageServe)))

	mux.Handle("GET /admin/scrape", admin(http.HandlerFunc(s.handleScrapeConfigPage)))
	mux.Handle("POST /admin/scrape/{storeID}/save", admin(http.HandlerFunc(s.handleScrapeConfigSave)))
	mux.Handle("POST /admin/scrape/{storeID}/test", admin(http.HandlerFunc(s.handleScrapeConfigTest)))
	mux.Handle("GET /admin/scrape/proxy", admin(http.HandlerFunc(s.handleScrapeProxy)))
	mux.Handle("POST /admin/scrape/fetch", admin(http.HandlerFunc(s.handleScrapeFetch)))
	mux.Handle("POST /admin/scrape/selector", admin(http.HandlerFunc(s.handleScrapeSelector)))
	mux.Handle("POST /admin/scrape/ai-extract", admin(http.HandlerFunc(s.handleScrapeAIExtract)))
	mux.Handle("POST /admin/scrape/autodetect", admin(http.HandlerFunc(s.handleScrapeAutodetect)))
	mux.Handle("POST /admin/scrape/{storeID}/pin-product", admin(http.HandlerFunc(s.handleScrapePinProduct)))

	// Hybrid CAPTCHA solve: a live CDP session the admin watches and clicks
	// through, or pasting cookies solved elsewhere - see
	// web/handlers_scrape_live.go.
	mux.Handle("POST /admin/scrape/{storeID}/live/start", admin(http.HandlerFunc(s.handleScrapeLiveStart)))
	mux.Handle("GET /admin/scrape/live/{sessionID}/ws", admin(http.HandlerFunc(s.handleScrapeLiveWS)))
	mux.Handle("POST /admin/scrape/live/{sessionID}/finish", admin(http.HandlerFunc(s.handleScrapeLiveFinish)))
	mux.Handle("POST /admin/scrape/live/{sessionID}/cancel", admin(http.HandlerFunc(s.handleScrapeLiveCancel)))
	mux.Handle("POST /admin/scrape/{storeID}/clearance/manual", admin(http.HandlerFunc(s.handleScrapeClearanceManual)))

	mux.Handle("GET /settings", admin(http.HandlerFunc(s.handleSettingsPage)))
	mux.Handle("POST /settings/danger/{target}", own(http.HandlerFunc(s.handleDangerWipe)))
	mux.Handle("GET /about", admin(http.HandlerFunc(s.handleAbout)))
	mux.Handle("GET /about/probe", admin(http.HandlerFunc(s.handleAboutProbe)))
	mux.Handle("POST /settings/save", admin(http.HandlerFunc(s.handleSettingsSave)))
	mux.Handle("POST /settings/test-render", admin(http.HandlerFunc(s.handleSettingsTestRender)))
	mux.Handle("POST /settings/test-ai", admin(http.HandlerFunc(s.handleSettingsTestAI)))
	mux.Handle("POST /settings/test-kroger", admin(http.HandlerFunc(s.handleSettingsTestKroger)))
	mux.Handle("POST /settings/ha/test", admin(http.HandlerFunc(s.handleHATest)))
	mux.Handle("POST /settings/ha/entities", admin(http.HandlerFunc(s.handleHAEntities)))
	mux.Handle("POST /settings/ha/save", admin(http.HandlerFunc(s.handleHASave)))
	mux.Handle("POST /settings/ai/models", admin(http.HandlerFunc(s.handleAIModels)))
	mux.Handle("GET /admin/llm-debug", admin(http.HandlerFunc(s.handleLLMDebugLog)))
	mux.Handle("GET /admin/llm-log", admin(http.HandlerFunc(s.handleLLMLogPage)))
	mux.Handle("GET /admin/events", admin(http.HandlerFunc(s.handleAdminEventsPage)))
	mux.Handle("GET /search", view(http.HandlerFunc(s.handleSearch)))
	mux.Handle("GET /attributions", view(http.HandlerFunc(s.handleAttributionsPage)))

	mux.Handle("GET /scan", view(http.HandlerFunc(s.handleScanPage)))
	mux.Handle("GET /scan/{code}", view(http.HandlerFunc(s.handleScanCode)))
	mux.Handle("POST /pantry/scan", edit(http.HandlerFunc(s.handlePantryScan)))

	// Finance dashboard - literal routes before the /finance/{...} wildcards.
	mux.Handle("GET /finance", view(http.HandlerFunc(s.handleFinanceOverview)))
	mux.Handle("GET /finance/ai", view(http.HandlerFunc(s.handleFinanceAI)))
	mux.Handle("GET /finance/prices", view(http.HandlerFunc(s.handleFinancePrices)))
	mux.Handle("POST /finance/ai/reference", admin(http.HandlerFunc(s.handleFinanceRefSave)))
	mux.Handle("POST /finance/ai/reference/{id}/delete", admin(http.HandlerFunc(s.handleFinanceRefDelete)))
	mux.Handle("POST /finance/ai/mapping", admin(http.HandlerFunc(s.handleFinanceMappingSave)))

	mux.Handle("GET /", view(http.HandlerFunc(s.handleDashboard)))
}
