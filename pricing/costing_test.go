package pricing

import (
	"context"
	"testing"

	"goeat/db"
)

func newCostingStore(t *testing.T) (db.Store, *db.Household) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(context.Background(), db.CreateHouseholdParams{Name: "T", WeeklyBudgetCents: 10000})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	return store, hh
}

// TestCostPlan_FallsBackToLLMEstimate reproduces the "canned black beans: 38"
// mystery report: with no stores configured (so the resolution chain never
// answers) and no manual/cached price on file, CostPlan used to zero out the
// line entirely. It should now fall back to the price the meal-planning LLM
// guessed for this ingredient at generation time (db.MealIngredient.EstPriceCents)
// instead of leaving it at $0.
func TestCostPlan_FallsBackToLLMEstimate(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)

	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 10000})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	meal, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "dinner", Title: "Chili", Effort: "quick", Servings: 2, CookedPortions: 2})
	if err != nil {
		t.Fatalf("meal: %v", err)
	}
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "canned black beans", Quantity: 1, Unit: "can", EstPriceCents: 129,
	}); err != nil {
		t.Fatalf("ingredient: %v", err)
	}

	chain := NewChain(nil) // no providers configured - nothing can answer
	if _, err := CostPlan(ctx, store, chain, p.ID, hh, nil); err != nil {
		t.Fatalf("cost plan: %v", err)
	}

	items, err := store.ListShoppingListItems(ctx, p.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d shopping list items, want 1", len(items))
	}
	line := items[0]
	if line.UnitPriceCents != 129 {
		t.Fatalf("unit price = %d, want 129 (the LLM's own estimate)", line.UnitPriceCents)
	}
	if line.LineTotalCents != 129 {
		t.Fatalf("line total = %d, want 129", line.LineTotalCents)
	}
	if line.PurchaseUnit != "can" {
		t.Fatalf("purchase unit = %q, want %q", line.PurchaseUnit, "can")
	}
	if line.Confidence != ConfidenceEstimate {
		t.Fatalf("confidence = %q, want %q", line.Confidence, ConfidenceEstimate)
	}
}

// TestCostPlan_ZeroWhenNoEstimateAvailable confirms the true last-resort path
// (no provider, no LLM estimate either) still degrades to a $0 estimate rather
// than erroring, so old data (ingredients persisted before est_price_cents
// existed) keeps behaving exactly as it did before.
func TestCostPlan_ZeroWhenNoEstimateAvailable(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)

	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 10000})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	meal, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "dinner", Title: "Chili", Effort: "quick", Servings: 2, CookedPortions: 2})
	if err != nil {
		t.Fatalf("meal: %v", err)
	}
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "canned black beans", Quantity: 1, Unit: "can",
	}); err != nil {
		t.Fatalf("ingredient: %v", err)
	}

	chain := NewChain(nil)
	if _, err := CostPlan(ctx, store, chain, p.ID, hh, nil); err != nil {
		t.Fatalf("cost plan: %v", err)
	}

	items, _ := store.ListShoppingListItems(ctx, p.ID)
	if len(items) != 1 {
		t.Fatalf("got %d shopping list items, want 1", len(items))
	}
	if items[0].UnitPriceCents != 0 || items[0].LineTotalCents != 0 {
		t.Fatalf("expected $0 with no estimate on file, got unit=%d total=%d",
			items[0].UnitPriceCents, items[0].LineTotalCents)
	}
}
