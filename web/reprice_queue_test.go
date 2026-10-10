package web

import (
	"context"
	"testing"
	"time"

	"goeat/db"
	"goeat/pricing"
)

// A change made while a plan's shopping list is already being rebuilt is not
// lost: its rebuild is queued and runs when the one in flight finishes. The
// case that showed it was a recipe edited while its swap was still being
// priced - the ingredient the edit removed stayed on the list.
func TestRepriceQueuedBehindARunInFlight(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()
	planID := f.a.meal.PlanID

	for _, name := range []string{"ground beef", "worcestershire sauce"} {
		if err := f.store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID: f.a.meal.ID, Name: name, Quantity: 1, Unit: "each", NormalizedTerm: pricing.Normalize(name),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pricing.SeedShoppingList(ctx, f.store, planID, f.a.hh); err != nil {
		t.Fatal(err)
	}

	// A rebuild is in flight (stood in for by its flag) when the recipe loses
	// an ingredient and asks for another.
	f.srv.repriceMu.Lock()
	f.srv.repricingPlans[planID] = true
	f.srv.repriceMu.Unlock()

	if err := f.store.DeleteMealIngredients(ctx, f.a.meal.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: f.a.meal.ID, Name: "ground beef", Quantity: 1, Unit: "each", NormalizedTerm: pricing.Normalize("ground beef"),
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.repriceInBackground(planID, f.a.hh, true)

	if rows, _ := f.store.ListShoppingListItems(ctx, planID); len(rows) != 2 {
		t.Fatalf("list changed while the first run was still in flight: %d lines", len(rows))
	}

	// The run in flight finishes; the queued one starts by itself.
	f.srv.repriceDone(planID)
	for i := 0; f.srv.isRepricing(planID); i++ {
		if i > 500 {
			t.Fatal("queued rebuild did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	rows, _ := f.store.ListShoppingListItems(ctx, planID)
	if len(rows) != 1 || rows[0].DisplayName != "ground beef" {
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.DisplayName)
		}
		t.Errorf("shopping list = %v, want only ground beef", names)
	}
}
