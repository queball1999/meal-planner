package db

import "time"

// User represents a household member account (§9.1, §10.1).
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string // "admin" | "read_only"
	TOTPEnabled  bool
	CreatedAt    time.Time
}

// Session is an authenticated session token (§9.3, §10.1).
type Session struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	IPAddress string
	UserAgent string
	CreatedAt time.Time
}

// Household is the single household row (§10.1). In v1 there is always at
// most one; the table is kept for the multi-tenant migration path.
type Household struct {
	ID                int64
	Name              string
	WeeklyBudgetCents int64
	Country           string
	ZIPCode           string
	RegionLabel       string
	Timezone          string
	HouseholdSize     int
	CreatedAt         time.Time
}

// CreateHouseholdParams bundles the household creation inputs (§4.5, §10.1).
type CreateHouseholdParams struct {
	Name              string
	WeeklyBudgetCents int64
	Country           string
	ZIPCode           string
	Timezone          string
	HouseholdSize     int
}

// AppEvent is a single audit-log entry (§9.3, §10.1).
type AppEvent struct {
	ActorUserID *int64
	ActorLabel  string
	Action      string
	TargetType  string
	TargetID    string
	Metadata    string // JSON blob or empty string
	IPAddress   string
	UserAgent   string
	Status      string // "ok" | "error"
}

// ── Phase 2 — Preferences (§4, §10.1) ────────────────────────────────────────

// GroceryStore is a store the household shops at (§10.1).
// Named GroceryStore to avoid collision with the Store interface.
type GroceryStore struct {
	ID            int64
	HouseholdID   int64
	Name          string
	Kind          string // "grocery" | "warehouse" | "specialty" | "online"
	ProviderChain string // comma-separated provider list
	Enabled       bool
	CreatedAt     time.Time
}

// Preferences holds the household's structured preference settings (§4.2, §4.3).
// Diet tags, cuisines, and dislikes are stored as JSON arrays in SQLite.
type Preferences struct {
	ID               int64
	HouseholdID      int64
	DietTags         []string // "vegetarian" | "vegan" | "pescatarian" | "keto" | "low-carb" | "gluten-free"
	Cuisines         []string
	Dislikes         []string
	LeftoverTolerance bool
	UpdatedAt        time.Time
}

// MealSlotHint holds the free-text description and LLM parse for one meal slot (§4.1).
type MealSlotHint struct {
	ID          int64
	HouseholdID int64
	Slot        string // "breakfast" | "lunch" | "dinner"
	RawText     string
	ParsedJSON  string // JSON string as stored; "null" when not yet parsed
	Effort      string // "quick" | "standard" | "elaborate"
	UpdatedAt   time.Time
}

// MealFeedback is a thumbs-up/down rating on a planned meal (§4.4).
type MealFeedback struct {
	ID           int64
	HouseholdID  int64
	MealID       *int64 // nil when rated outside a plan context
	Title        string
	Rating       int    // -1 = dislike, 1 = like
	TagsSnapshot []string
	CreatedAt    time.Time
}

// AIRun records one LLM call for cost tracking and observability (§7.7, §10.1).
type AIRun struct {
	ID               int64
	HouseholdID      int64
	Purpose          string // "free_text_parse" | "generation" | "price_estimate" | "normalize"
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	EstCostCents     int
	Status           string // "ok" | "error"
	CreatedAt        time.Time
}

// UpsertStoreParams bundles inputs for creating or updating a store.
type UpsertStoreParams struct {
	HouseholdID int64
	Name        string
	Kind        string
}

// UpsertPreferencesParams bundles inputs for updating preferences.
type UpsertPreferencesParams struct {
	HouseholdID       int64
	DietTags          []string
	Cuisines          []string
	Dislikes          []string
	LeftoverTolerance bool
}

// UpsertMealSlotHintParams bundles inputs for saving a free-text slot hint.
type UpsertMealSlotHintParams struct {
	HouseholdID int64
	Slot        string
	RawText     string
	ParsedJSON  string
	Effort      string
}

// CreateFeedbackParams bundles inputs for recording meal feedback.
type CreateFeedbackParams struct {
	HouseholdID  int64
	MealID       *int64
	Title        string
	Rating       int
	TagsSnapshot []string
}

// CreateAIRunParams bundles inputs for recording an AI run.
type CreateAIRunParams struct {
	HouseholdID      int64
	Purpose          string
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	EstCostCents     int
	Status           string
}

// ── Phase 3 — Plans & meals (§5, §7, §10.1) ──────────────────────────────────

// Plan is one week's meal plan (§10.1). Retained after the week passes (§5.5).
type Plan struct {
	ID                int64
	HouseholdID       int64
	WeekStart         string // YYYY-MM-DD
	WeekEnd           string // YYYY-MM-DD
	BudgetCents       int64
	TotalCents        int64
	ConfidenceSummary string
	Status            string // "generating" | "ready" | "error"
	CreatedAt         time.Time
}

// Meal is one slot in a plan (§10.1).
type Meal struct {
	ID                   int64
	PlanID               int64
	Day                  string // YYYY-MM-DD
	Slot                 string // "breakfast" | "lunch" | "dinner"
	Title                string
	Effort               string // "quick" | "standard" | "elaborate"
	Servings             int
	CookedPortions       int
	IsLeftover           bool
	LeftoverSourceMealID *int64
	Locked               bool
	AIRunID              *int64
}

// MealRecipe holds steps for one meal (§5.2, §10.1).
type MealRecipe struct {
	ID        int64
	MealID    int64
	StepsJSON string // JSON array of step strings
	Servings  int
	Notes     string
}

// MealIngredient is one ingredient line on a meal (§10.1).
type MealIngredient struct {
	ID             int64
	MealID         int64
	Name           string
	Quantity       float64
	Unit           string
	NormalizedTerm string
}

// CreatePlanParams bundles inputs for creating a plan.
type CreatePlanParams struct {
	HouseholdID int64
	WeekStart   string
	WeekEnd     string
	BudgetCents int64
}

// CreateMealParams bundles inputs for creating a meal.
type CreateMealParams struct {
	PlanID         int64
	Day            string
	Slot           string
	Title          string
	Effort         string
	Servings       int
	CookedPortions int
	AIRunID        *int64
}

// CreateMealRecipeParams bundles inputs for creating a meal recipe.
type CreateMealRecipeParams struct {
	MealID    int64
	StepsJSON string
	Servings  int
	Notes     string
}

// CreateMealIngredientParams bundles inputs for creating one ingredient.
type CreateMealIngredientParams struct {
	MealID   int64
	Name     string
	Quantity float64
	Unit     string
}

// ── Phase 4 — Pricing (§6, §10.1) ────────────────────────────────────────────

// PriceCache is one observed price in the system-of-record (§6.0, §6.5).
type PriceCache struct {
	ID             int64
	StoreID        int64
	NormalizedTerm string
	PriceCents     int64
	PurchaseUnit   string
	PackSize       float64
	Source         string // "live"|"cache"|"manual"|"scrape"|"estimate"
	Confidence     string // "live"|"cached"|"manual"|"scrape"|"estimate"
	FetchedAt      time.Time
}

// ManualPrice is an operator-entered price for a store + region (§6.2).
type ManualPrice struct {
	ID             int64
	StoreID        int64
	Region         string // ZIP/metro text, not a FK
	NormalizedTerm string
	PriceCents     int64
	PackSize       float64
	PurchaseUnit   string
	UpdatedBy      string
	UpdatedAt      time.Time
}

// ItemProductMap caches the resolved product for a (store, normalized_term) pair (§6.3).
type ItemProductMap struct {
	ID             int64
	StoreID        int64
	NormalizedTerm string
	ChosenProduct  string
	PackSize       float64
	PurchaseUnit   string
	Barcode        string
	UpdatedAt      time.Time
}

// ScrapeConfig holds the per-store scrape configuration built by §6.7.
type ScrapeConfig struct {
	ID                int64
	StoreID           int64
	SearchURLTemplate string // {term} placeholder
	SelectorsJSON     string // JSON: {name, price, pack_size, availability}
	Mode              string // "assisted"|"auto"|"auto_ai"
	AIAssisted        bool
	Status            string // "active"|"degraded"|"unconfigured"
	LastTestedAt      string // ISO timestamp or ""
	CreatedAt         time.Time
}

// ShoppingListItem is one priced buy-line on a plan's shopping list (§5.1, §6.4).
type ShoppingListItem struct {
	ID                  int64
	PlanID              int64
	StoreID             *int64
	MealIngredientRefs  string // JSON []int64
	DisplayName         string
	BuyQuantity         float64
	PackSize            float64
	PurchaseUnit        string
	UnitPriceCents      int64
	LineTotalCents      int64
	PriceSource         string // "live"|"cache"|"manual"|"scrape"|"estimate"
	Confidence          string
	Checked             bool
	InPantry            bool
}

// UpsertPriceCacheParams bundles inputs for writing a price to the cache.
type UpsertPriceCacheParams struct {
	StoreID        int64
	NormalizedTerm string
	PriceCents     int64
	PurchaseUnit   string
	PackSize       float64
	Source         string
	Confidence     string
}

// UpsertManualPriceParams bundles inputs for an operator-entered price.
type UpsertManualPriceParams struct {
	StoreID        int64
	Region         string
	NormalizedTerm string
	PriceCents     int64
	PackSize       float64
	PurchaseUnit   string
	UpdatedBy      string
}

// UpsertItemProductMapParams bundles inputs for caching a product match.
type UpsertItemProductMapParams struct {
	StoreID        int64
	NormalizedTerm string
	ChosenProduct  string
	PackSize       float64
	PurchaseUnit   string
	Barcode        string
}

// CreateScrapeConfigParams bundles inputs for a new scrape config.
type CreateScrapeConfigParams struct {
	StoreID           int64
	SearchURLTemplate string
	SelectorsJSON     string
	Mode              string
	AIAssisted        bool
}

// UpdateScrapeConfigParams bundles editable fields on a scrape config.
type UpdateScrapeConfigParams struct {
	ID                int64
	SearchURLTemplate string
	SelectorsJSON     string
	Mode              string
	AIAssisted        bool
	Status            string
	LastTestedAt      string
}

// CreateShoppingListItemParams bundles inputs for one shopping list line.
type CreateShoppingListItemParams struct {
	PlanID             int64
	StoreID            *int64
	MealIngredientRefs string
	DisplayName        string
	BuyQuantity        float64
	PackSize           float64
	PurchaseUnit       string
	UnitPriceCents     int64
	LineTotalCents     int64
	PriceSource        string
	Confidence         string
}

// ── Phase 5.5 — Spend stats (§5.5) ───────────────────────────────────────────

// SpendStats aggregates spend and usage numbers over a date range (§5.5).
type SpendStats struct {
	TotalCents  int64
	BudgetCents int64
	MealCount   int64
	PlanCount   int
}

// ── Phase 5 — Pantry & plan-day overrides (§5.4, §5.6, §10.1) ───────────────

// PantryItem is one ingredient the household has on hand (§5.4).
type PantryItem struct {
	ID              int64
	HouseholdID     int64
	Name            string
	NormalizedTerm  string
	QuantityOnHand  float64
	Unit            string
	Barcode         string
	UpdatedAt       time.Time
}

// PlanDay holds per-day overrides (headcount, notes) for one day of a plan (§5.6).
type PlanDay struct {
	ID        int64
	PlanID    int64
	Date      string // YYYY-MM-DD
	Headcount int
	Note      string
}

// CreatePantryItemParams bundles inputs for adding a pantry item.
type CreatePantryItemParams struct {
	HouseholdID    int64
	Name           string
	NormalizedTerm string
	QuantityOnHand float64
	Unit           string
	Barcode        string
}

// UpdatePantryItemParams bundles editable fields on a pantry item.
type UpdatePantryItemParams struct {
	ID             int64
	QuantityOnHand float64
	Unit           string
}

// UpsertPlanDayParams bundles inputs for setting a plan-day headcount override.
type UpsertPlanDayParams struct {
	PlanID    int64
	Date      string
	Headcount int
	Note      string
}
