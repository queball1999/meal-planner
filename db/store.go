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
	GetLatestPlan(ctx context.Context, householdID int64) (*Plan, error)
	GetPlanByID(ctx context.Context, planID int64) (*Plan, error)

	// ── Meals (§5.2, §5.3, §10.1) ────────────────────────────────────────────

	CreateMeal(ctx context.Context, p CreateMealParams) (*Meal, error)
	ListMealsByPlan(ctx context.Context, planID int64) ([]*Meal, error)
	UpdateMealLocked(ctx context.Context, mealID int64, locked bool) error

	// ── Meal recipes (§5.2, §10.1) ───────────────────────────────────────────

	CreateMealRecipe(ctx context.Context, p CreateMealRecipeParams) error
	GetMealRecipe(ctx context.Context, mealID int64) (*MealRecipe, error)

	// ── Meal ingredients (§5.2, §6.3, §10.1) ────────────────────────────────

	CreateMealIngredient(ctx context.Context, p CreateMealIngredientParams) error
	ListIngredientsByMeal(ctx context.Context, mealID int64) ([]*MealIngredient, error)
	ListIngredientsByPlan(ctx context.Context, planID int64) ([]*MealIngredient, error)

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
	CheckShoppingListItem(ctx context.Context, id int64, checked bool) error
	DeleteShoppingListItems(ctx context.Context, planID int64) error

	// ── Plan total (§6.4) ─────────────────────────────────────────────────────

	UpdatePlanTotal(ctx context.Context, planID int64, totalCents int64, confidenceSummary string) error

	// ── Meals — additional (§5.2, §5.6, §7.6) ───────────────────────────────

	GetMealByID(ctx context.Context, mealID int64) (*Meal, error)
	UpdateMealLeftover(ctx context.Context, mealID int64, isLeftover bool, sourceMealID *int64) error
	UpdateMealTitle(ctx context.Context, mealID int64, title, effort string, servings, cookedPortions int) error
	DeleteMealIngredients(ctx context.Context, mealID int64) error

	// ── Plan days (§5.6) ──────────────────────────────────────────────────────

	ListPlanDays(ctx context.Context, planID int64) ([]*PlanDay, error)
	UpsertPlanDay(ctx context.Context, p UpsertPlanDayParams) error
	GetPlanDay(ctx context.Context, planID int64, date string) (*PlanDay, error)

	// ── Pantry items (§5.4) ───────────────────────────────────────────────────

	CreatePantryItem(ctx context.Context, p CreatePantryItemParams) (*PantryItem, error)
	ListPantryItems(ctx context.Context, householdID int64) ([]*PantryItem, error)
	UpdatePantryItem(ctx context.Context, p UpdatePantryItemParams) error
	DeletePantryItem(ctx context.Context, id int64) error

	// ── Shopping list — pantry flag (§5.4) ───────────────────────────────────

	MarkShoppingListItemInPantry(ctx context.Context, id int64, inPantry bool) error
}
