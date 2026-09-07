package db

import (
	"context"
	"time"
)

// Store is the application's complete data-access interface. All SQL lives in
// this package; no handler or service writes its own queries (§10, §12).
//
// Phase 0: Ping, Migrate, Close.
// Phase 1: User, Session, Household, AppEvent methods.
// Later phases add Plan, Meal, Pricing, etc.
type Store interface {
	// ── Lifecycle ──────────────────────────────────────────────────────────

	Ping(ctx context.Context) error
	Migrate() error
	Close() error

	// ── Users (§9.1, §10.1) ───────────────────────────────────────────────

	CreateUser(ctx context.Context, username, passwordHash, role string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)

	// ── Sessions (§9.3, §10.1) ────────────────────────────────────────────

	CreateSession(ctx context.Context, userID int64, tokenHash, ipAddr, userAgent string, expiresAt time.Time) (*Session, error)
	GetSessionByTokenHash(ctx context.Context, hash string) (*Session, error)
	DeleteSession(ctx context.Context, id int64) error
	DeleteUserSessions(ctx context.Context, userID int64) error

	// ── Household (§10.1) ─────────────────────────────────────────────────

	// CreateHousehold creates the single household row.
	CreateHousehold(ctx context.Context, p CreateHouseholdParams) (*Household, error)

	// GetHousehold returns nil, nil when no household exists yet (setup not done).
	GetHousehold(ctx context.Context) (*Household, error)

	// ── Audit log (§9.3) ──────────────────────────────────────────────────

	LogEvent(ctx context.Context, e AppEvent) error

	// ── Stores (§10.1) ────────────────────────────────────────────────────

	CreateStore(ctx context.Context, p UpsertStoreParams) (*GroceryStore, error)
	ListStores(ctx context.Context, householdID int64) ([]*GroceryStore, error)
	DeleteStore(ctx context.Context, id int64) error

	// ── Preferences (§4.2, §4.3, §10.1) ──────────────────────────────────

	// UpsertPreferences replaces the household's preferences row (insert or replace).
	UpsertPreferences(ctx context.Context, p UpsertPreferencesParams) error

	// GetPreferences returns the household's preferences, or a zero-value struct
	// with defaults when no row exists yet.
	GetPreferences(ctx context.Context, householdID int64) (*Preferences, error)

	// ── Allergies (§4.2, §10.1) ───────────────────────────────────────────

	// SetAllergies replaces all allergy rows for the household atomically.
	SetAllergies(ctx context.Context, householdID int64, terms []string) error

	// ListAllergies returns all allergy terms for the household.
	ListAllergies(ctx context.Context, householdID int64) ([]string, error)

	// ── Meal slot hints (§4.1, §10.1) ────────────────────────────────────

	// UpsertMealSlotHint inserts or replaces the hint for one slot.
	UpsertMealSlotHint(ctx context.Context, p UpsertMealSlotHintParams) error

	// GetMealSlotHints returns all three slot rows, creating empty ones when absent.
	GetMealSlotHints(ctx context.Context, householdID int64) ([]*MealSlotHint, error)

	// ── Meal feedback (§4.4, §10.1) ───────────────────────────────────────

	CreateFeedback(ctx context.Context, p CreateFeedbackParams) (*MealFeedback, error)

	// ListFeedbackDigest returns up to limit most-recent feedback rows for prompt building.
	ListFeedbackDigest(ctx context.Context, householdID int64, limit int) ([]*MealFeedback, error)

	// ── AI runs (§7.7, §10.1) ─────────────────────────────────────────────

	CreateAIRun(ctx context.Context, p CreateAIRunParams) (*AIRun, error)

	// ── Plans (§5, §7.3, §10.1) ──────────────────────────────────────────────

	CreatePlan(ctx context.Context, p CreatePlanParams) (*Plan, error)
	UpdatePlanStatus(ctx context.Context, planID int64, status string) error
	DeletePlan(ctx context.Context, householdID, planID int64) error
	DeleteAllPlansForHousehold(ctx context.Context, householdID int64) error
	CancelOtherPlansForWeek(ctx context.Context, householdID int64, weekStart string, keepPlanID int64) error
	GetLatestPlan(ctx context.Context, householdID int64) (*Plan, error)
	GetPlanByID(ctx context.Context, planID int64) (*Plan, error)
	ListPlans(ctx context.Context, householdID int64) ([]*Plan, error)
	ListPlansInRange(ctx context.Context, householdID int64, from, to string) ([]*Plan, error)
	GetPlanByWeekStart(ctx context.Context, householdID int64, weekStart string) (*Plan, error)

	// ── Meals (§5.2, §5.3, §10.1) ────────────────────────────────────────────

	CreateMeal(ctx context.Context, p CreateMealParams) (*Meal, error)
	ListMealsByPlan(ctx context.Context, planID int64) ([]*Meal, error)
	ListMealsByHouseholdRange(ctx context.Context, householdID int64, from, to string) ([]*Meal, error)
	UpdateMealLocked(ctx context.Context, mealID int64, locked bool) error

	// ── Meal recipes (§5.2, §10.1) ───────────────────────────────────────────

	CreateMealRecipe(ctx context.Context, p CreateMealRecipeParams) error
	GetMealRecipe(ctx context.Context, mealID int64) (*MealRecipe, error)

	// ── Meal ingredients (§5.2, §6.3, §10.1) ────────────────────────────────

	CreateMealIngredient(ctx context.Context, p CreateMealIngredientParams) error
	SetMealIngredientItem(ctx context.Context, ingredientID int64, itemID *int64, normalizedTerm string) error
	ListIngredientsByMeal(ctx context.Context, mealID int64) ([]*MealIngredient, error)
	ListIngredientsByPlan(ctx context.Context, planID int64) ([]*MealIngredient, error)
	ListMealTitlesByIngredientID(ctx context.Context, planID int64) (map[int64]string, error)

	// ── Price cache (§6.0, §6.5) ──────────────────────────────────────────────

	UpsertPriceCache(ctx context.Context, p UpsertPriceCacheParams) error
	GetPriceCache(ctx context.Context, storeID int64, normalizedTerm string) (*PriceCache, error)

	// ── Manual prices (§6.2 ManualProvider) ──────────────────────────────────

	UpsertManualPrice(ctx context.Context, p UpsertManualPriceParams) error
	GetManualPrice(ctx context.Context, storeID int64, region, normalizedTerm string) (*ManualPrice, error)
	ListManualPrices(ctx context.Context, storeID int64) ([]*ManualPrice, error)
	DeleteManualPrice(ctx context.Context, id int64) error

	// ── Item↔product map (§6.3) ───────────────────────────────────────────────

	UpsertItemProductMap(ctx context.Context, p UpsertItemProductMapParams) error
	GetItemProductMap(ctx context.Context, storeID int64, normalizedTerm string) (*ItemProductMap, error)

	// ── Scrape configs (§6.7) ─────────────────────────────────────────────────

	CreateScrapeConfig(ctx context.Context, p CreateScrapeConfigParams) (*ScrapeConfig, error)
	GetScrapeConfigByStore(ctx context.Context, storeID int64) (*ScrapeConfig, error)
	ListScrapeConfigs(ctx context.Context) ([]*ScrapeConfig, error)
	UpdateScrapeConfig(ctx context.Context, p UpdateScrapeConfigParams) error

	// ── Shopping list (§5.1, §6.4) ───────────────────────────────────────────

	CreateShoppingListItem(ctx context.Context, p CreateShoppingListItemParams) (*ShoppingListItem, error)
	ListShoppingListItems(ctx context.Context, planID int64) ([]*ShoppingListItem, error)
	GetShoppingListItem(ctx context.Context, id int64) (*ShoppingListItem, error)
	UpdateShoppingListItemPrice(ctx context.Context, p UpdateShoppingListItemPriceParams) error
	CheckShoppingListItem(ctx context.Context, id int64, checked bool) error
	DeleteShoppingListItems(ctx context.Context, planID int64) error
	DeleteAllShoppingListItemsForHousehold(ctx context.Context, householdID int64) error

	// ── Plan total (§6.4) ─────────────────────────────────────────────────────

	UpdatePlanTotal(ctx context.Context, planID int64, totalCents int64, confidenceSummary string) error

	// ── Spend stats (§5.5) ────────────────────────────────────────────────────

	GetSpendStats(ctx context.Context, householdID int64, from, to string) (*SpendStats, error)

	// ── Meals - additional (§5.2, §5.6, §7.6) ───────────────────────────────

	GetMealByID(ctx context.Context, mealID int64) (*Meal, error)
	UpdateMealLeftover(ctx context.Context, mealID int64, isLeftover bool, sourceMealID *int64) error
	UpdateMealTitle(ctx context.Context, mealID int64, title, effort string, servings, cookedPortions int) error
	DeleteMealIngredients(ctx context.Context, mealID int64) error

	// ── Plan days (§5.6) ──────────────────────────────────────────────────────

	ListPlanDays(ctx context.Context, planID int64) ([]*PlanDay, error)
	ListHouseholdMembers(ctx context.Context, householdID int64) ([]*HouseholdMember, error)
	GetHouseholdMember(ctx context.Context, householdID, id int64) (*HouseholdMember, error)
	CreateHouseholdMember(ctx context.Context, p CreateHouseholdMemberParams) (int64, error)
	UpdateHouseholdMember(ctx context.Context, p UpdateHouseholdMemberParams) error
	DeleteHouseholdMember(ctx context.Context, householdID, id int64) error
	SumPortionFactors(ctx context.Context, householdID int64, ids []int64) (float64, int, error)
	CountHouseholdMembers(ctx context.Context, householdID int64) (int, error)

	UpsertPlanDay(ctx context.Context, p UpsertPlanDayParams) error
	GetPlanDay(ctx context.Context, planID int64, date string) (*PlanDay, error)
	ScaleMealsForDay(ctx context.Context, planID int64, date string, portions float64) (ScaleDayResult, error)

	// ── Pantry items (§5.4) ───────────────────────────────────────────────────

	CreatePantryItem(ctx context.Context, p CreatePantryItemParams) (*PantryItem, error)
	ListPantryItems(ctx context.Context, householdID int64) ([]*PantryItem, error)
	UpdatePantryItem(ctx context.Context, p UpdatePantryItemParams) error
	DeletePantryItem(ctx context.Context, id int64) error
	DeleteAllPantryItemsForHousehold(ctx context.Context, householdID int64) error
	SetPantryItemItem(ctx context.Context, id int64, itemID *int64) error

	// ── Shopping list - pantry flag (§5.4) ───────────────────────────────────

	MarkShoppingListItemInPantry(ctx context.Context, id int64, inPantry bool) error

	// ── Recipe catalog (§5.7) ─────────────────────────────────────────────────

	CreateCatalogRecipe(ctx context.Context, p CreateCatalogRecipeParams) (*CatalogRecipe, error)
	GetCatalogRecipe(ctx context.Context, id int64) (*CatalogRecipe, error)
	ListCatalogRecipes(ctx context.Context, householdID int64) ([]*CatalogRecipe, error)
	FilterCatalogRecipes(ctx context.Context, householdID int64, f CatalogRecipeFilter) ([]*CatalogRecipe, error)
	DeleteCatalogRecipe(ctx context.Context, id int64) error
	DeleteAllCatalogRecipesForHousehold(ctx context.Context, householdID int64) error
	UpdateCatalogRecipe(ctx context.Context, p UpdateCatalogRecipeParams) error
	ClearCatalogRecipeImage(ctx context.Context, id int64) error
	DeleteCatalogRecipeIngredients(ctx context.Context, catalogRecipeID int64) error
	DeleteCatalogRecipeSteps(ctx context.Context, catalogRecipeID int64) error
	AddCatalogRecipeIngredient(ctx context.Context, catalogRecipeID int64, name, quantity, unit string, position int) error
	ListCatalogRecipeIngredients(ctx context.Context, catalogRecipeID int64) ([]*CatalogRecipeIngredient, error)
	AddCatalogRecipeStep(ctx context.Context, catalogRecipeID int64, position int, text string) error
	ListCatalogRecipeSteps(ctx context.Context, catalogRecipeID int64) ([]*CatalogRecipeStep, error)

	// ── Items catalog + quantity conversions (00010_items.sql) ───────────────

	CreateItem(ctx context.Context, p CreateItemParams) (*Item, error)
	GetItem(ctx context.Context, id int64) (*Item, error)
	GetItemByTerm(ctx context.Context, householdID int64, term string) (*Item, error)
	ListItems(ctx context.Context, householdID int64) ([]*Item, error)
	FilterItems(ctx context.Context, householdID int64, f ItemFilter) ([]*Item, error)
	UpdateItem(ctx context.Context, p UpdateItemParams) error
	DeleteItem(ctx context.Context, id int64) error
	SetItemImage(ctx context.Context, id int64, imagePath, attribution string) error
	ClearItemImage(ctx context.Context, id int64) error

	UpsertUnitConversion(ctx context.Context, p UpsertUnitConversionParams) error
	ListGlobalConversions(ctx context.Context) ([]*UnitConversion, error)
	ListConversionsForItem(ctx context.Context, itemID int64) ([]*UnitConversion, error)
	DeleteUnitConversion(ctx context.Context, id int64) error
	ReplaceDerivedItemConversions(ctx context.Context, itemID int64, edges []UpsertUnitConversionParams) error

	UpsertItemStorePackage(ctx context.Context, p UpsertItemStorePackageParams) error
	ListPackagesForItem(ctx context.Context, itemID int64) ([]*ItemStorePackage, error)
	ListPackagesForStore(ctx context.Context, storeID int64) ([]*ItemStorePackage, error)
	GetItemStorePackage(ctx context.Context, itemID, storeID int64) (*ItemStorePackage, error)
	DeleteItemStorePackage(ctx context.Context, id int64) error
	ListPriceHistory(ctx context.Context, itemID, storeID int64) ([]*PriceHistoryEntry, error)

	// ── Search (§8.4c) ────────────────────────────────────────────────────────

	SearchCatalogRecipes(ctx context.Context, householdID int64, q string) ([]*CatalogRecipe, error)
	SearchPantryItems(ctx context.Context, householdID int64, q string) ([]*PantryItem, error)
	SearchMealTitles(ctx context.Context, householdID int64, q string) ([]*Meal, error)

	// ── Pantry - barcode & filter (§5.8) ──────────────────────────────────────

	FilterPantryItems(ctx context.Context, householdID int64, q string) ([]*PantryItem, error)
	GetPantryItemByBarcode(ctx context.Context, householdID int64, code string) (*PantryItem, error)
	IncrementPantryItem(ctx context.Context, id int64, delta float64) error

	// ── Item product map - barcode (§8.4d) ────────────────────────────────────

	GetItemProductMapByBarcode(ctx context.Context, code string) (*ItemProductMap, error)

	// ── Settings (see package settings) ───────────────────────────────────────

	SeedSetting(ctx context.Context, key, value string) error
	SetSetting(ctx context.Context, key, value string) error
	ListSettings(ctx context.Context) ([]*Setting, error)
	GetSetting(ctx context.Context, key string) (*Setting, error)

	// ── Encrypted secrets (see package cryptbox) ─────────────────────────────

	SetSecret(ctx context.Context, key, ciphertext string) error
	GetSecret(ctx context.Context, key string) (string, bool, error)

	// ── Home Assistant sync map (00012_ha_sync.sql) ─────────────────────────

	UpsertHASyncRow(ctx context.Context, p UpsertHASyncRowParams) (*HASyncRow, error)
	ListHASyncRows(ctx context.Context, householdID int64) ([]*HASyncRow, error)
	GetHASyncByTerm(ctx context.Context, householdID int64, term string) (*HASyncRow, error)
	SetHASyncPushed(ctx context.Context, id int64, haUID, haStatus, summary string, localChecked bool) error
	SetHASyncPulled(ctx context.Context, id int64, haStatus string, localChecked bool) error
	DeleteHASyncRow(ctx context.Context, id int64) error
}
