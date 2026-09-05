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
}
