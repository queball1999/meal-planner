package db_test

import (
	"context"
	"testing"

	"goeat/db"
)

// newDangerWipeStore seeds two households, each with one recipe, one pantry
// item, and one plan (with a meal and a shopping-list line) - so a wipe for
// household A can be checked against household B staying untouched.
func newDangerWipeStore(t *testing.T) (store db.Store, hhA, hhB *db.Household) {
	t.Helper()
	s, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	hhA, err = s.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "A"})
	if err != nil {
		t.Fatalf("household A: %v", err)
	}
	hhB, err = s.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "B"})
	if err != nil {
		t.Fatalf("household B: %v", err)
	}

	seed := func(hh *db.Household) {
		if _, err := s.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
			HouseholdID: hh.ID, Title: "Chili", SourceKind: "manual", Servings: 4,
		}); err != nil {
			t.Fatalf("seed recipe (%s): %v", hh.Name, err)
		}
		if _, err := s.CreatePantryItem(ctx, db.CreatePantryItemParams{
			HouseholdID: hh.ID, Name: "Rice", NormalizedTerm: "rice", QuantityOnHand: 2, Unit: "lb",
		}); err != nil {
			t.Fatalf("seed pantry (%s): %v", hh.Name, err)
		}
		p, err := s.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
		if err != nil {
			t.Fatalf("seed plan (%s): %v", hh.Name, err)
		}
		if _, err := s.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID: p.ID, DisplayName: "rice", BuyQuantity: 2, PurchaseUnit: "lb",
			LineTotalCents: 199, PriceSource: "estimate", Confidence: "estimate",
		}); err != nil {
			t.Fatalf("seed shopping list item (%s): %v", hh.Name, err)
		}
		if err := s.UpdatePlanTotal(ctx, p.ID, 199, "100% estimated"); err != nil {
			t.Fatalf("seed plan total (%s): %v", hh.Name, err)
		}
	}
	seed(hhA)
	seed(hhB)
	return s, hhA, hhB
}

func TestDeleteAllCatalogRecipesForHousehold(t *testing.T) {
	ctx := context.Background()
	store, hhA, hhB := newDangerWipeStore(t)

	if err := store.DeleteAllCatalogRecipesForHousehold(ctx, hhA.ID); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	a, _ := store.ListCatalogRecipes(ctx, hhA.ID)
	b, _ := store.ListCatalogRecipes(ctx, hhB.ID)
	if len(a) != 0 {
		t.Fatalf("household A still has %d recipes", len(a))
	}
	if len(b) != 1 {
		t.Fatalf("household B recipes = %d, want 1 (untouched)", len(b))
	}
}

func TestDeleteAllPantryItemsForHousehold(t *testing.T) {
	ctx := context.Background()
	store, hhA, hhB := newDangerWipeStore(t)

	if err := store.DeleteAllPantryItemsForHousehold(ctx, hhA.ID); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	a, _ := store.ListPantryItems(ctx, hhA.ID)
	b, _ := store.ListPantryItems(ctx, hhB.ID)
	if len(a) != 0 {
		t.Fatalf("household A still has %d pantry items", len(a))
	}
	if len(b) != 1 {
		t.Fatalf("household B pantry = %d, want 1 (untouched)", len(b))
	}
}

func TestDeleteAllShoppingListItemsForHousehold(t *testing.T) {
	ctx := context.Background()
	store, hhA, hhB := newDangerWipeStore(t)

	planA, err := store.GetLatestPlan(ctx, hhA.ID)
	if err != nil || planA == nil {
		t.Fatalf("get plan A: %v", err)
	}

	if err := store.DeleteAllShoppingListItemsForHousehold(ctx, hhA.ID); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	itemsA, _ := store.ListShoppingListItems(ctx, planA.ID)
	if len(itemsA) != 0 {
		t.Fatalf("household A plan still has %d shopping list items", len(itemsA))
	}
	refreshedA, err := store.GetPlanByID(ctx, planA.ID)
	if err != nil || refreshedA == nil {
		t.Fatalf("get plan A: %v", err)
	}
	if refreshedA.TotalCents != 0 {
		t.Fatalf("plan A total = %d, want 0 after clearing its shopping list", refreshedA.TotalCents)
	}

	planB, err := store.GetLatestPlan(ctx, hhB.ID)
	if err != nil || planB == nil {
		t.Fatalf("get plan B: %v", err)
	}
	itemsB, _ := store.ListShoppingListItems(ctx, planB.ID)
	if len(itemsB) != 1 {
		t.Fatalf("household B shopping list items = %d, want 1 (untouched)", len(itemsB))
	}
	if planB.TotalCents != 199 {
		t.Fatalf("household B plan total = %d, want untouched at 199", planB.TotalCents)
	}
}

func TestDeleteAllPlansForHousehold(t *testing.T) {
	ctx := context.Background()
	store, hhA, hhB := newDangerWipeStore(t)

	if err := store.DeleteAllPlansForHousehold(ctx, hhA.ID); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	a, _ := store.ListPlans(ctx, hhA.ID)
	b, _ := store.ListPlans(ctx, hhB.ID)
	if len(a) != 0 {
		t.Fatalf("household A still has %d plans", len(a))
	}
	if len(b) != 1 {
		t.Fatalf("household B plans = %d, want 1 (untouched)", len(b))
	}
}
