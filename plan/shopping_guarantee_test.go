package plan

import (
	"context"
	"testing"
)

// A plan is not usable without something to shop from. Pricing is optional
// (nil pricer here, as when no pricing chain is configured) and its failures
// are non-fatal, so Generate must fall back to an unpriced list rather than
// leaving the plan with nothing.
func TestGenerate_AlwaysWritesAShoppingList(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := Generate(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	items, err := store.ListShoppingListItems(ctx, planID)
	if err != nil {
		t.Fatalf("list shopping items: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("plan finished with an empty shopping list")
	}
	for _, it := range items {
		if it.DisplayName == "" {
			t.Errorf("shopping line %d has no display name", it.ID)
		}
		if it.BuyQuantity <= 0 {
			t.Errorf("shopping line %q has buy quantity %v, want > 0", it.DisplayName, it.BuyQuantity)
		}
	}
}

// Every day of the generated week gets an explicit headcount row, which is
// what per-day portion scaling keys off.
func TestGenerate_SeedsPlanDays(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := Generate(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	days, err := store.ListPlanDays(ctx, planID)
	if err != nil {
		t.Fatalf("list plan days: %v", err)
	}
	if len(days) != 7 {
		t.Fatalf("got %d plan days, want 7", len(days))
	}
	for _, d := range days {
		if d.Headcount != 2 { // the test household's size
			t.Errorf("%s headcount = %d, want 2", d.Date, d.Headcount)
		}
	}
}

// persistPlan must record the generated yield and quantities as the baseline
// that later headcount changes scale from.
func TestGenerate_RecordsScalingBaseline(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := Generate(ctx, store, &fakeGenerator{titlePrefix: "X"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil || len(meals) == 0 {
		t.Fatalf("list meals: %v", err)
	}
	for _, m := range meals {
		if m.BaseServings != m.Servings {
			t.Fatalf("meal %d: base_servings %d != servings %d", m.ID, m.BaseServings, m.Servings)
		}
		if m.BaseCookedPortions != m.CookedPortions {
			t.Fatalf("meal %d: base_cooked_portions %d != cooked_portions %d", m.ID, m.BaseCookedPortions, m.CookedPortions)
		}
	}

	ings, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil || len(ings) == 0 {
		t.Fatalf("list ingredients: %v", err)
	}
	for _, ing := range ings {
		if ing.BaseQuantity != ing.Quantity {
			t.Fatalf("ingredient %q: base_quantity %v != quantity %v", ing.Name, ing.BaseQuantity, ing.Quantity)
		}
	}
}
