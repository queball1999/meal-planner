package db

import "time"

// User is a login (§9.1, §10.1). Role is the *instance* role - see
// InstanceRoleAdmin; what a user may do inside a household is the
// HouseholdMembership.Role for that household.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string // InstanceRoleAdmin | InstanceRoleMember
	TOTPEnabled  bool
	CreatedAt    time.Time
}

// Instance roles (users.role). An admin runs the server - settings, AI keys,
// scraper tooling, accounts - and is an implicit owner of every household.
const (
	InstanceRoleAdmin  = "admin"
	InstanceRoleMember = "member"
)

// IsAdmin reports whether u is an instance admin. Nil-safe.
func (u *User) IsAdmin() bool { return u != nil && u.Role == InstanceRoleAdmin }

// Household roles (household_memberships.role), weakest first.
const (
	HouseholdRoleViewer = "viewer"
	HouseholdRoleEditor = "editor"
	HouseholdRoleOwner  = "owner"
)

// HouseholdRoleRank orders household roles so a gate can ask "at least
// editor". Unknown roles rank 0 - below viewer - so a typo denies.
func HouseholdRoleRank(role string) int {
	switch role {
	case HouseholdRoleViewer:
		return 1
	case HouseholdRoleEditor:
		return 2
	case HouseholdRoleOwner:
		return 3
	}
	return 0
}

// ValidHouseholdRole reports whether role is one of the three household roles.
func ValidHouseholdRole(role string) bool { return HouseholdRoleRank(role) > 0 }

// HouseholdMembership is one user's seat in one household.
type HouseholdMembership struct {
	HouseholdID   int64
	HouseholdName string
	UserID        int64
	Username      string
	Role          string
	CreatedAt     time.Time
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
	// LastSeenAt is the last page load that counted as activity (at most once
	// a minute; background polling doesn't count). Idle timeout reads it.
	LastSeenAt time.Time

	// ActiveHouseholdID is the household this session is looking at; 0 means
	// "none chosen yet" and middleware falls back to the first membership.
	ActiveHouseholdID int64
}

// Household is one tenant (§10.1). Every household-scoped table carries
// household_id; users reach a household through HouseholdMembership.
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
	// SharePct is roughly how much of the household's shopping happens here,
	// 0-100 (00033_store_shares.sql). ListStores orders by it, so every caller
	// that walks stores in order - pricing above all - tries the primary
	// store first.
	SharePct  int
	CreatedAt time.Time
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
	// UnitSystem drives shopping-list display of weight/volume quantities:
	// "as-is" (default, no conversion) | "metric" | "imperial".
	UnitSystem string
	UpdatedAt  time.Time
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
	Purpose          string // "free_text_parse" | "generation" | "price_estimate" | "normalize" | "video_recipe"
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
	UnitSystem        string // "as-is" | "metric" | "imperial"
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

// ── Finance dashboard (§7.7, §10.1) ──────────────────────────────────────────

// PricingReference is a reference price for one (provider, model) pair, stored
// per-million tokens (00029_pricing_reference.sql). It is a *reference* - what
// the usage would have cost on a third-party provider - not a bill.
type PricingReference struct {
	ID                    int64
	Provider              string
	ReferenceModel        string
	InputPricePerMillion  float64
	OutputPricePerMillion float64
	EffectiveDate         string // YYYY-MM-DD
	Notes                 string
	UpdatedAt             time.Time
}

// ModelCostMapping maps a local model/purpose label to a pricing reference.
type ModelCostMapping struct {
	LocalModel         string
	PricingReferenceID int64
	Provider           string // denormalized for display
	ReferenceModel     string // denormalized for display
	UpdatedAt          time.Time
}

// UpsertPricingReferenceParams bundles inputs for upserting a reference price.
type UpsertPricingReferenceParams struct {
	Provider              string
	ReferenceModel        string
	InputPricePerMillion  float64
	OutputPricePerMillion float64
	EffectiveDate         string
	Notes                 string
}

// AIRunTotals aggregates AI usage and cost over a date range.
type AIRunTotals struct {
	RunCount         int
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	EstCostCents     int64
	ErrorCount       int
}

// AIRunByPurpose is one purpose's usage/cost rollup (the "by purpose" table).
type AIRunByPurpose struct {
	Purpose          string
	RunCount         int
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	EstCostCents     int64
}

// AIRunByModel is one model's usage/cost rollup (the "by model" table).
type AIRunByModel struct {
	Provider         string
	Model            string
	RunCount         int
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	EstCostCents     int64
}

// AIRunDaily is one day's usage/cost (the trend chart series).
type AIRunDaily struct {
	Date         string // YYYY-MM-DD
	RunCount     int
	TotalTokens  int64
	EstCostCents int64
}

// PriceTrackingRow is one item's current price across stores, with its most
// recent history, for the finance "price tracking" table.
type PriceTrackingRow struct {
	ItemID           int64
	ItemName         string
	StoreID          int64
	StoreName        string
	PriceCents       int64
	PurchaseUnit     string
	AmountPerPackage float64
	Preferred        bool
	UpdatedAt        time.Time
	// History is the item's recent price readings (oldest first) for a sparkline.
	History []PriceHistoryEntry
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
	// Status is "cooking" | "eating_out" | "skipped" (00026_meal_status.sql) -
	// the meal-card icons on the Plan page. A non-cooking meal's ingredients
	// are excluded from the shopping list (ListIngredientsByPlan).
	Status string
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

	// BlockReason/BlockURL/BlockedAt record a genuine bot-wall hit (Cloudflare/
	// Incapsula/PerimeterX/CAPTCHA signature) that survived the full automated
	// FlareSolverr + Browserless chain - distinct from Status "degraded"
	// (selectors found nothing), which no human solving a CAPTCHA would fix.
	// A non-empty BlockedAt is what the Scrape Config page treats as "blocked"
	// rather than a new Status enum value (its CHECK constraint would need a
	// full table rebuild to widen - see db/migrations/00027 for the same
	// tradeoff made with a separate flag instead of a new status).
	// Set by MarkScrapeConfigBlocked, cleared by ClearScrapeConfigBlocked.
	BlockReason string
	BlockURL    string
	BlockedAt   string // ISO timestamp or ""
}

// Blocked reports whether this store's scraper is waiting on a human to solve
// a bot wall (see ScrapeConfig.BlockedAt).
func (sc *ScrapeConfig) Blocked() bool {
	return sc != nil && sc.BlockedAt != ""
}

// ScrapeClearance holds cookies (+ the user agent they were issued to) an
// admin obtained by solving a store's challenge themselves - live via
// scrape/live, or pasted in manually - reused by ScraperProvider.Lookup ahead
// of the automated chain until ExpiresAt. One row per store
// (db/migrations/00031_scrape_captcha.sql).
type ScrapeClearance struct {
	StoreID     int64
	CookiesJSON string // JSON []scrape.Cookie
	UserAgent   string
	ObtainedAt  time.Time
	ExpiresAt   time.Time
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
	ItemID             *int64  // catalog item, nil when unlinked (00010_items.sql)
	PantryQtyUsed      float64 // how much the household already had (00022)
	// Pending marks a skeleton row CostPlan seeded but has not yet priced
	// (00027) - the shopping list tab renders these as loading placeholders.
	Pending bool
	// PackAmount/PackUnit are this line's raw "amount per package" and its
	// unit, before pricing.reconcilePack converts it into the ingredient's own
	// stock unit for PackSize/BuyQuantity (00030). Zero/empty means this line
	// was never resolved against a real discrete package - a flat guess for
	// exactly the quantity it was priced at. pricing.RescaleShoppingList reads
	// these to adjust an already-priced line for a new required quantity
	// (guest count, meal skip) without asking a provider again.
	PackAmount float64
	PackUnit   string
	// NeedQuantity is what the plan's recipes call for, in the stock unit,
	// before pantry stock (00037) - the number the list shows; PantryQtyUsed
	// is how much of it the pantry covers. BuyQuantity is what is left,
	// rounded up to whole packs. 0 means not recorded (a pre-00037 row
	// not yet backfilled).
	NeedQuantity float64
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
	PantryQtyUsed  float64
	InPantry       bool
	// PackAmount/PackUnit: see ShoppingListItem. Zero/"" for a manual edit or
	// any other write that has no real package behind it.
	PackAmount float64
	PackUnit   string
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

	// PantryQtyUsed is how much of this line the household's existing stock
	// covered (00022). Recorded so the list can say "1 of 3 lb from your
	// pantry" rather than silently showing a smaller number - a quantity that
	// shrinks with no explanation reads as a bug in the plan.
	PantryQtyUsed float64
	// InPantry marks a line the pantry covered entirely: kept on the list, out
	// of the total. The same state the "I already have this" control sets.
	InPantry bool
	// Pending marks a skeleton row CostPlan seeds before pricing runs (00027)
	// - the shopping list tab renders these as loading placeholders until
	// ResolvePricing turns each one into a priced line.
	Pending bool
	// NeedQuantity: see ShoppingListItem.NeedQuantity.
	NeedQuantity float64
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
	// PreferredStoreID is "we only buy this here" (00033_store_shares.sql):
	// pricing looks the item up at this store and no other. nil = any store.
	PreferredStoreID *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	// Preferred marks this as the store the household chooses to buy this item
	// from, when it has package rows at more than one (00025_item_store_preferred.sql).
	// Untouched by UpsertItemStorePackage - only SetItemStorePreferred changes it,
	// so re-saving a price never silently un-prefers a store.
	Preferred bool
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
	// Guests is how many non-household people are eating that day, on top of
	// the members. Each guest counts as one standard portion in the serving
	// maths (see UpsertPlanDay), and the plan page renders it as the
	// "Guests x N" control.
	Guests int
	// GuestSlots is the set of slots ("breakfast"|"lunch"|"dinner") the guests
	// are counted for. Empty means every slot - the default, and what a day
	// with guests but no explicit slot choice means.
	GuestSlots []string
	// Status is "cooking" (the default), "eating_out", or "skipped". A day
	// that is not being cooked contributes nothing to the shopping list.
	Status string
}

// Day statuses for PlanDay.Status. Mirrors the CHECK constraint in
// migrations/00019_day_status.sql.
const (
	DayCooking   = "cooking"
	DayEatingOut = "eating_out"
	DaySkipped   = "skipped"
)

// ValidDayStatus reports whether s is a status the plan_days CHECK will
// accept, so a bad form value comes back as a message rather than a raw
// SQLite constraint error.
func ValidDayStatus(s string) bool {
	return s == DayCooking || s == DayEatingOut || s == DaySkipped
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

	// Guests is how many non-household people are eating that day. Each adds
	// one standard portion to the day's total.
	Guests int
	// GuestSlots is which slots the guests eat ("breakfast"|"lunch"|"dinner").
	// Empty means every slot.
	GuestSlots []string
}
