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

// Setting is one runtime-editable configuration value (see package settings).
// Source is "env" for a value seeded from .env at first boot, or "admin" once
// it has been edited via the Settings page.
type Setting struct {
	Key       string
	Value     string
	Source    string
	UpdatedAt time.Time
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

// ── Phase 2 - Preferences (§4, §10.1) ────────────────────────────────────────

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
	ID                int64
	HouseholdID       int64
	DietTags          []string // "vegetarian" | "vegan" | "pescatarian" | "keto" | "low-carb" | "gluten-free"
	Cuisines          []string
	Dislikes          []string
	LeftoverTolerance bool
	UpdatedAt         time.Time
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
	Rating       int // -1 = dislike, 1 = like
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

// ── Phase 3 - Plans & meals (§5, §7, §10.1) ──────────────────────────────────

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
	Canceled          bool   // true once superseded by a regenerate for the same week (00016) - kept for history
	CreatedAt         time.Time
}

// Meal is one slot in a plan (§10.1).
type Meal struct {
	ID             int64
	PlanID         int64
	Day            string // YYYY-MM-DD
	Slot           string // "breakfast" | "lunch" | "dinner"
	Title          string
	Effort         string // "quick" | "standard" | "elaborate"
	Servings       int
	CookedPortions int
	// Base* hold the meal as the LLM generated it. Per-day headcount changes
	// rescale Servings/CookedPortions (and every ingredient quantity) from
	// these, never from the current values, so repeated adjustments are
	// idempotent instead of drifting (00017).
	BaseServings         int
	BaseCookedPortions   int
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
	BaseQuantity   float64 // as-generated amount; Quantity = BaseQuantity × headcount/base_servings (00017)
	Unit           string
	NormalizedTerm string
	ItemID         *int64 // catalog item, nil when unlinked (00010_items.sql)
	EstPriceCents  int64  // the plan-generation LLM's own price guess for this quantity (00015)
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

// ScaleDayResult reports what ScaleMealsForDay changed, so a caller can decide
// whether the plan's shopping list needs rebuilding.
type ScaleDayResult struct {
	MealsScaled       int
	IngredientsScaled int
}

// CreateMealRecipeParams bundles inputs for creating a meal recipe.
type CreateMealRecipeParams struct {
	MealID    int64
	StepsJSON string
	Servings  int
	Notes     string
}

// CreateMealIngredientParams bundles inputs for creating one ingredient.
// NormalizedTerm and ItemID are optional: leave them zero to store an unlinked
// row (the catalog linking pass fills them in later).
type CreateMealIngredientParams struct {
	MealID         int64
	Name           string
	Quantity       float64
	Unit           string
	NormalizedTerm string
	ItemID         *int64
	EstPriceCents  int64
}

// ── Phase 4 - Pricing (§6, §10.1) ────────────────────────────────────────────

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
	// ContextJSON is the store context this retailer needs before its search
	// page shows anything: {"cookies":{…},"prewarm":[…],"wait_for":"…"}.
	// See db/migrations/00009_scrape_context.sql.
	ContextJSON  string
	AIAssisted   bool
	Status       string // "active"|"degraded"|"unconfigured"
	LastTestedAt string // ISO timestamp or ""
	CreatedAt    time.Time
}

// ShoppingListItem is one priced buy-line on a plan's shopping list (§5.1, §6.4).
type ShoppingListItem struct {
	ID                 int64
	PlanID             int64
	StoreID            *int64
	MealIngredientRefs string // JSON []int64
	DisplayName        string
	BuyQuantity        float64
	PackSize           float64
	PurchaseUnit       string
	UnitPriceCents     int64
	LineTotalCents     int64
	PriceSource        string // "live"|"cache"|"manual"|"scrape"|"estimate"
	Confidence         string
	Checked            bool
	InPantry           bool
	ItemID             *int64 // catalog item, nil when unlinked (00010_items.sql)
}

// UpdateShoppingListItemPriceParams rewrites one shopping-list line's price and
// resolved store/item after a manual edit via the pencil-icon price modal.
type UpdateShoppingListItemPriceParams struct {
	ID             int64
	StoreID        *int64
	ItemID         *int64
	BuyQuantity    float64
	PackSize       float64
	PurchaseUnit   string
	UnitPriceCents int64
	LineTotalCents int64
	PriceSource    string
	Confidence     string
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
	ContextJSON       string
	AIAssisted        bool
}

// UpdateScrapeConfigParams bundles editable fields on a scrape config.
type UpdateScrapeConfigParams struct {
	ID                int64
	SearchURLTemplate string
	SelectorsJSON     string
	Mode              string
	ContextJSON       string
	AIAssisted        bool
	Status            string
	LastTestedAt      string
}

// CreateShoppingListItemParams bundles inputs for one shopping list line.
type CreateShoppingListItemParams struct {
	PlanID             int64
	StoreID            *int64
	ItemID             *int64
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

// ── Items catalog + quantity conversions (00010_items.sql) ───────────────────

// Item is one canonical grocery item in a household's catalog. It gives a
// normalized_term a real row with metadata: category, photo, a stock unit it is
// aggregated/held in, and a default purchase quantity.
type Item struct {
	ID                 int64
	HouseholdID        int64
	Name               string
	NormalizedTerm     string
	Category           string
	StockUnit          string // unit the item is aggregated/held in (Grocy QU_STOCK)
	DefaultPurchaseQty float64
	ImagePath          string // relative path under ITEM_IMAGE_DIR; "" = none
	ImageAttribution   string // required credit line for the photo
	ImageSourceURL     string // where the photo is lazily fetched from
	Source             string // "builtin" | "manual" | "auto"
	Notes              string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// UnitConversion is one edge in the quantity-conversion graph: 1 FromUnit =
// Factor ToUnit. ItemID nil is a global conversion (mass, volume); a non-nil
// ItemID is an item-specific bridge, usually count -> mass/volume.
type UnitConversion struct {
	ID       int64
	ItemID   *int64
	FromUnit string
	ToUnit   string
	Factor   float64
	Derived  bool // true = machine-generated bridge to an item's stock unit
}

// ItemStorePackage is how one item is sold at one store: the purchase unit, the
// amount per package (in that unit), and the price. Supersedes the pack_size /
// purchase_unit scalars on manual_prices / price_cache for catalogued items.
type ItemStorePackage struct {
	ID               int64
	ItemID           int64
	StoreID          int64
	PurchaseUnit     string
	AmountPerPackage float64
	PriceCents       int64
	UpdatedBy        string
	UpdatedAt        time.Time
}

// PriceHistoryEntry is one recorded price for an (item, store) pair over time
// (00014_price_history.sql). Written every time UpsertItemStorePackage runs, so
// it captures edits from the item detail page, the admin prices page, and the
// shopping list's pencil-icon price editor alike.
type PriceHistoryEntry struct {
	ID               int64
	ItemID           int64
	StoreID          int64
	PriceCents       int64
	PurchaseUnit     string
	AmountPerPackage float64
	RecordedBy       string
	RecordedAt       time.Time
}

// ItemFilter holds optional filters for FilterItems.
type ItemFilter struct {
	Q        string // name text search
	Category string // exact category match
}

// CreateItemParams bundles inputs for creating a catalog item.
type CreateItemParams struct {
	HouseholdID        int64
	Name               string
	NormalizedTerm     string
	Category           string
	StockUnit          string
	DefaultPurchaseQty float64
	ImageSourceURL     string
	ImageAttribution   string
	Source             string // "builtin" | "manual" | "auto"; defaults to "auto"
	Notes              string
}

// UpdateItemParams bundles the editable fields of a catalog item. An empty
// ImagePath leaves the existing image untouched.
type UpdateItemParams struct {
	ID                 int64
	Name               string
	Category           string
	StockUnit          string
	DefaultPurchaseQty float64
	Notes              string
	ImagePath          string
}

// UpsertUnitConversionParams bundles inputs for one conversion edge. ItemID nil
// writes a global conversion.
type UpsertUnitConversionParams struct {
	ItemID   *int64
	FromUnit string
	ToUnit   string
	Factor   float64
}

// UpsertItemStorePackageParams bundles inputs for one per-store package.
type UpsertItemStorePackageParams struct {
	ItemID           int64
	StoreID          int64
	PurchaseUnit     string
	AmountPerPackage float64
	PriceCents       int64
	UpdatedBy        string
}

// ── Phase 5.7 - Recipe catalog (§5.7, §10.1) ─────────────────────────────────

// CatalogRecipe is one entry in the household's recipe catalog (§5.7).
type CatalogRecipe struct {
	ID          int64
	HouseholdID int64
	Title       string
	SourceKind  string // "ai" | "imported" | "manual"
	SourceURL   string
	SourceSite  string
	ImagePath   string // relative path under RECIPE_IMAGE_DIR; "" = no image
	Servings    int
	PrepMinutes int
	CookMinutes int
	Tags        []string
	CreatedAt   time.Time
}

// CatalogRecipeIngredient is one ingredient line in a catalog recipe.
type CatalogRecipeIngredient struct {
	ID              int64
	CatalogRecipeID int64
	Name            string
	Quantity        string
	Unit            string
	NormalizedTerm  string
	Position        int
}

// CatalogRecipeStep is one instruction step in a catalog recipe.
type CatalogRecipeStep struct {
	ID              int64
	CatalogRecipeID int64
	Position        int
	Text            string
}

// CatalogRecipeFilter holds optional filters for FilterCatalogRecipes (§8.4c).
type CatalogRecipeFilter struct {
	Q      string // title / tags text search
	Tag    string // exact tag match
	Source string // "ai" | "imported" | "manual"
}

// CreateCatalogRecipeParams bundles inputs for creating a catalog recipe.
type CreateCatalogRecipeParams struct {
	HouseholdID int64
	Title       string
	SourceKind  string
	SourceURL   string
	SourceSite  string
	ImagePath   string
	Servings    int
	PrepMinutes int
	CookMinutes int
	Tags        []string
}

// UpdateCatalogRecipeParams bundles the editable fields of a catalog recipe.
// An empty ImagePath leaves the existing image untouched.
type UpdateCatalogRecipeParams struct {
	ID          int64
	Title       string
	Servings    int
	PrepMinutes int
	CookMinutes int
	Tags        []string
	ImagePath   string
}

// ── Phase 5.5 - Spend stats (§5.5) ───────────────────────────────────────────

// SpendStats aggregates spend and usage numbers over a date range (§5.5).
type SpendStats struct {
	TotalCents  int64
	BudgetCents int64
	MealCount   int64
	PlanCount   int
}

// ── Phase 5 - Pantry & plan-day overrides (§5.4, §5.6, §10.1) ───────────────

// PantryItem is one ingredient the household has on hand (§5.4).
type PantryItem struct {
	ID             int64
	HouseholdID    int64
	Name           string
	NormalizedTerm string
	QuantityOnHand float64
	Unit           string
	Barcode        string
	UpdatedAt      time.Time
	ItemID         *int64 // catalog item, nil when unlinked (00010_items.sql)
}

// PlanDay holds per-day overrides (headcount, notes) for one day of a plan (§5.6).
type PlanDay struct {
	ID        int64
	PlanID    int64
	Date      string // YYYY-MM-DD
	Headcount int    // count of people eating - the number shown to the user
	Note      string

	// Which household members are eating, and the portion total their factors
	// add up to. Portions is what the serving maths actually uses; Headcount is
	// len(MemberIDs) when members are chosen, and stands alone for a household
	// that has never set any up.
	//
	// Portions is stored rather than recomputed from MemberIDs on read:
	// editing a member's portion factor next month must not silently restate
	// what a plan from last month was scaled to.
	MemberIDs []int64
	Portions  float64
}

// HouseholdMember is one person the household cooks for (§4.5). PortionFactor
// is how much they eat relative to one standard adult serving - a small child
// near 0.5, a light eater 0.8, a big eater 1.4 - so a plan for two adults and
// two toddlers budgets 3.0 portions rather than 4.
type HouseholdMember struct {
	ID            int64
	HouseholdID   int64
	Name          string
	PortionFactor float64
	Notes         string
	SortOrder     int
	CreatedAt     time.Time
}

// CreateHouseholdMemberParams bundles the inputs for adding a member.
type CreateHouseholdMemberParams struct {
	HouseholdID   int64
	Name          string
	PortionFactor float64
	Notes         string
	SortOrder     int
}

// UpdateHouseholdMemberParams bundles the editable fields on a member.
// HouseholdID is carried so the UPDATE is scoped to the caller's household and
// an id from another one cannot be edited by guessing it.
type UpdateHouseholdMemberParams struct {
	ID            int64
	HouseholdID   int64
	Name          string
	PortionFactor float64
	Notes         string
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

	// MemberIDs is who is eating; Portions is what their factors add up to.
	// A zero Portions means "no member selection was made", and the upsert
	// falls back to Headcount standard portions so a caller that predates
	// members (or a household with none) keeps working unchanged.
	MemberIDs []int64
	Portions  float64
}
