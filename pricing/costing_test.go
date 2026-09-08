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

type fakeProvider struct{ r *PriceResult }

func (f fakeProvider) Name() string { return "fake" }
func (f fakeProvider) Lookup(_ context.Context, _ string, _ int64, _ string) (*PriceResult, error) {
	return f.r, nil
}

func oneLine(t *testing.T, store db.Store, planID int64) *db.ShoppingListItem {
	t.Helper()
	items, err := store.ListShoppingListItems(context.Background(), planID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d shopping list items, want 1", len(items))
	}
	return items[0]
}

// TestCostPlan_ReconcilesPackUnitToStockUnit is the "681 lb of chicken breast"
// regression. The chain prices chicken by the pound, the catalog item stocks it
// in grams, and PacksNeeded used to divide 680 (grams) by a 1 (pound) pack size
// and buy 680 "lb". The pack has to be converted into the stock unit first.
func TestCostPlan_ReconcilesPackUnitToStockUnit(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)
	gs, err := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: "S", Kind: "grocery"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	it, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hh.ID, Name: "chicken breast", NormalizedTerm: Normalize("chicken breast"),
		StockUnit: "g", DefaultPurchaseQty: 1, Source: "manual",
	})
	if err != nil {
		t.Fatalf("item: %v", err)
	}

	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 100000})
	meal, _ := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "dinner", Title: "X", Effort: "quick", Servings: 2, CookedPortions: 2})
	id := it.ID
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "chicken breast", Quantity: 680, Unit: "g",
		NormalizedTerm: it.NormalizedTerm, ItemID: &id,
	}); err != nil {
		t.Fatalf("ingredient: %v", err)
	}

	chain := NewChain([]PriceProvider{fakeProvider{&PriceResult{
		PriceCents: 400, PurchaseUnit: "lb", PackSize: 1, Source: "live", Confidence: ConfidenceLive,
	}}})
	if _, err := CostPlan(ctx, store, chain, p.ID, hh, []*db.GroceryStore{gs}); err != nil {
		t.Fatalf("cost plan: %v", err)
	}

	line := oneLine(t, store, p.ID)
	// 680 g need, 1 lb ≈ 453.59 g/pack -> 2 packs -> ~907 g, 2 x $4 = $8.
	if line.BuyQuantity < 900 || line.BuyQuantity > 910 {
		t.Errorf("buy quantity = %v, want ~907 g (2 one-pound packs), not a pounds/grams mixup", line.BuyQuantity)
	}
	if line.LineTotalCents != 800 {
		t.Errorf("line total = %d, want 800", line.LineTotalCents)
	}
}

// TestCostPlan_UnreconcilablePackDegradesToEstimate is the "9 cartons of eggs"
// regression. Eggs stock "each", the price comes back per "carton", and nothing
// on the conversion graph connects the two. Rather than buy 9 cartons, the line
// falls back to the recipe amount and is flagged an estimate.
func TestCostPlan_UnreconcilablePackDegradesToEstimate(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)
	gs, err := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: "S", Kind: "grocery"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	it, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID: hh.ID, Name: "eggs", NormalizedTerm: Normalize("eggs"),
		StockUnit: "each", DefaultPurchaseQty: 12, Source: "manual",
	})
	if err != nil {
		t.Fatalf("item: %v", err)
	}

	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 100000})
	meal, _ := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "breakfast", Title: "X", Effort: "quick", Servings: 2, CookedPortions: 2})
	id := it.ID
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "eggs", Quantity: 9, Unit: "each",
		NormalizedTerm: it.NormalizedTerm, ItemID: &id, EstPriceCents: 300,
	}); err != nil {
		t.Fatalf("ingredient: %v", err)
	}

	chain := NewChain([]PriceProvider{fakeProvider{&PriceResult{
		PriceCents: 600, PurchaseUnit: "carton", PackSize: 1, Source: "live", Confidence: ConfidenceLive,
	}}})
	if _, err := CostPlan(ctx, store, chain, p.ID, hh, []*db.GroceryStore{gs}); err != nil {
		t.Fatalf("cost plan: %v", err)
	}

	line := oneLine(t, store, p.ID)
	if line.BuyQuantity != 9 {
		t.Errorf("buy quantity = %v, want 9 (the recipe amount, not 9 cartons)", line.BuyQuantity)
	}
	if line.Confidence != ConfidenceEstimate {
		t.Errorf("confidence = %q, want %q (pack unit could not be reconciled)", line.Confidence, ConfidenceEstimate)
	}
	if line.LineTotalCents != 300 {
		t.Errorf("line total = %d, want 300 (the LLM per-quantity estimate, not 9 x $6)", line.LineTotalCents)
	}
}
