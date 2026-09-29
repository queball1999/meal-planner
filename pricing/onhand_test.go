package pricing_test

import (
	"context"
	"testing"

	"goeat/db"
	"goeat/pricing"
)

// Food named as on hand at generation covers the ingredients it plainly is,
// and nothing that merely shares a word with it.
func TestApplyOnHandMatching(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)

	items := []pricing.AggItem{
		{NormalizedTerm: "smoked chicken thigh", DisplayName: "Smoked chicken thighs", TotalQuantity: 2, Unit: "lb"},
		{NormalizedTerm: "brown rice", DisplayName: "Brown rice", TotalQuantity: 2, Unit: "cup"},
		{NormalizedTerm: "olive oil", DisplayName: "Olive oil", TotalQuantity: 3, Unit: "tbsp"},
		{NormalizedTerm: "garlic powder", DisplayName: "Garlic powder", TotalQuantity: 1, Unit: "tsp"},
		{NormalizedTerm: "chicken broth", DisplayName: "Chicken broth", TotalQuantity: 2, Unit: "cup"},
		{NormalizedTerm: "saffron", DisplayName: "Saffron", TotalQuantity: 1, Unit: "g"},
	}
	deducted := map[int]pricing.PantryDeduction{}
	pricing.ApplyOnHand(ctx, store, hhID, []string{"Chicken thighs", "rice", "Olive Oil", "garlic"}, items, deducted)

	want := []bool{true, true, true, false, false, false}
	for i, w := range want {
		if deducted[i].Covered != w {
			t.Errorf("%s covered = %v, want %v", items[i].DisplayName, deducted[i].Covered, w)
		}
	}
	// Marked like the list's own "I already have this": quantity untouched.
	if items[0].TotalQuantity != 2 || deducted[0].Used != 0 {
		t.Errorf("chicken thighs quantity/used = %v/%v, want 2/0", items[0].TotalQuantity, deducted[0].Used)
	}
}

// The on-hand list saved with the plan is honoured when the list is seeded,
// and a line ticked "I already have this" stays ticked through a reprice.
func TestSeedShoppingListHonoursOnHandAndTicks(t *testing.T) {
	ctx := context.Background()
	store, hhID := newPantryStore(t)
	hh, _ := store.GetHousehold(ctx, hhID)

	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-04", WeekEnd: "2026-01-10"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	m, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: p.ID, Day: "2026-01-05", Slot: "dinner", Title: "Paella",
		Effort: "standard", Servings: 2, CookedPortions: 2,
	})
	if err != nil {
		t.Fatalf("meal: %v", err)
	}
	for _, name := range []string{"olive oil", "saffron", "rice"} {
		if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID: m.ID, Name: name, Quantity: 1, Unit: "each", NormalizedTerm: pricing.Normalize(name),
		}); err != nil {
			t.Fatalf("ingredient %s: %v", name, err)
		}
	}
	if err := store.SetPlanOnHand(ctx, p.ID, []string{"Olive oil"}); err != nil {
		t.Fatalf("set on hand: %v", err)
	}

	have := func() map[string]bool {
		t.Helper()
		if _, err := pricing.SeedShoppingList(ctx, store, p.ID, hh); err != nil {
			t.Fatalf("seed: %v", err)
		}
		rows, _ := store.ListShoppingListItems(ctx, p.ID)
		out := map[string]bool{}
		for _, r := range rows {
			out[r.DisplayName] = r.InPantry
		}
		return out
	}

	got := have()
	if !got["olive oil"] || got["saffron"] || got["rice"] {
		t.Fatalf("after first seed = %v, want only olive oil marked", got)
	}

	rows, _ := store.ListShoppingListItems(ctx, p.ID)
	for _, r := range rows {
		if r.DisplayName == "saffron" {
			if err := store.MarkShoppingListItemInPantry(ctx, r.ID, true); err != nil {
				t.Fatalf("tick: %v", err)
			}
		}
	}

	got = have()
	if !got["olive oil"] || !got["saffron"] || got["rice"] {
		t.Errorf("after reprice = %v, want olive oil and saffron still marked", got)
	}
}
