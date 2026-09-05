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
