package pricing

import (
	"context"
	"testing"

	"goeat/db"
)

// countingProvider wraps a PriceProvider and counts how many times Lookup was
// called, so a test can assert a rescale never asks it again for a line it
// already priced.
type countingProvider struct {
	inner PriceProvider
	calls int
}

func (c *countingProvider) Name() string { return c.inner.Name() }
func (c *countingProvider) Lookup(ctx context.Context, term string, storeID int64, region string) (*PriceResult, error) {
	c.calls++
	return c.inner.Lookup(ctx, term, storeID, region)
}

// TestRescaleShoppingList_RescalesWithoutReaskingProvider is the "add a
// guest, don't reprice" case: a line that CostPlan already resolved against a
// live price must be scaled to the new required quantity by pack math alone,
// never by calling the provider again.
func TestRescaleShoppingList_RescalesWithoutReaskingProvider(t *testing.T) {
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

	provider := &countingProvider{inner: fakeProvider{&PriceResult{
		PriceCents: 400, PurchaseUnit: "lb", PackSize: 1, Source: "live", Confidence: ConfidenceLive,
	}}}
	chain := NewChain([]PriceProvider{provider})
	if _, err := CostPlan(ctx, store, chain, p.ID, hh, []*db.GroceryStore{gs}); err != nil {
		t.Fatalf("cost plan: %v", err)
	}
	if provider.calls == 0 {
		t.Fatalf("provider never called during initial CostPlan")
	}
	callsAfterCostPlan := provider.calls

	line := oneLine(t, store, p.ID)
	if line.LineTotalCents != 800 {
		t.Fatalf("line total after CostPlan = %d, want 800", line.LineTotalCents)
	}

	// Simulate doubling the household for the day (a guest count change):
	// the ingredient's required quantity doubles, same as
	// db.Store.ScaleMealsForDay would leave it.
	if err := store.DeleteMealIngredients(ctx, meal.ID); err != nil {
		t.Fatalf("delete ingredients: %v", err)
	}
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "chicken breast", Quantity: 1360, Unit: "g",
		NormalizedTerm: it.NormalizedTerm, ItemID: &id,
	}); err != nil {
		t.Fatalf("rescaled ingredient: %v", err)
	}

	if _, err := RescaleShoppingList(ctx, store, chain, p.ID, hh, []*db.GroceryStore{gs}); err != nil {
		t.Fatalf("rescale: %v", err)
	}

	if provider.calls != callsAfterCostPlan {
		t.Fatalf("provider called %d more time(s) during rescale, want 0 - an already-priced line must not be re-resolved", provider.calls-callsAfterCostPlan)
	}

	rescaledLine := oneLine(t, store, p.ID)
	// 1360 g needs ceil(1360/453.59) = 3 one-pound packs, same pack math
	// CostPlan itself would produce fresh at this quantity - x $4 = $12.
	if rescaledLine.LineTotalCents != 1200 {
		t.Errorf("line total after rescale = %d, want 1200 (3 packs at $4)", rescaledLine.LineTotalCents)
	}
	if rescaledLine.PriceSource != "live" {
		t.Errorf("price source after rescale = %q, want it to keep the original resolved source %q", rescaledLine.PriceSource, "live")
	}
}

// TestRescaleShoppingList_PricesNewIngredient confirms an ingredient that
// wasn't on the list before (a meal came back from eating-out) still gets a
// real price through the normal resolve pass, rather than being skipped.
func TestRescaleShoppingList_PricesNewIngredient(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)
	gs, err := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: "S", Kind: "grocery"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 100000})
	meal, _ := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "dinner", Title: "X", Effort: "quick", Servings: 2, CookedPortions: 2})
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: meal.ID, Name: "flour", Quantity: 1, Unit: "each", EstPriceCents: 60,
	}); err != nil {
		t.Fatalf("ingredient: %v", err)
	}

	provider := &countingProvider{inner: fakeProvider{&PriceResult{
		PriceCents: 250, PurchaseUnit: "each", PackSize: 1, Source: "live", Confidence: ConfidenceLive,
	}}}
	chain := NewChain([]PriceProvider{provider})

	if _, err := RescaleShoppingList(ctx, store, chain, p.ID, hh, []*db.GroceryStore{gs}); err != nil {
		t.Fatalf("rescale on an empty list: %v", err)
	}

	if provider.calls != 1 {
		t.Fatalf("provider called %d times pricing a brand-new line, want 1", provider.calls)
	}
	line := oneLine(t, store, p.ID)
	if line.LineTotalCents != 250 {
		t.Errorf("line total = %d, want 250", line.LineTotalCents)
	}
}
