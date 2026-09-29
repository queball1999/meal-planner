package db_test

import (
	"context"
	"testing"

	"goeat/db"
)

func newPlansTestStore(t *testing.T) (db.Store, *db.Household) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(context.Background(), db.CreateHouseholdParams{Name: "T"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	return store, hh
}

// TestCancelOtherPlansForWeek reproduces the regenerate flow: a second plan is
// created for the same week, and once it's confirmed ready the first plan
// should be flagged canceled - kept for /plan/history, but no longer surfaced
// by GetLatestPlan or GetPlanByWeekStart.
func TestCancelOtherPlansForWeek(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	first, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, first.ID, "ready"); err != nil {
		t.Fatalf("mark first ready: %v", err)
	}

	second, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, second.ID, "ready"); err != nil {
		t.Fatalf("mark second ready: %v", err)
	}

	if err := store.CancelOtherPlansForWeek(ctx, hh.ID, "2026-01-05", second.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	latest, err := store.GetLatestPlan(ctx, hh.ID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil || latest.ID != second.ID {
		t.Fatalf("latest plan = %+v, want id %d", latest, second.ID)
	}

	byWeek, err := store.GetPlanByWeekStart(ctx, hh.ID, "2026-01-05")
	if err != nil {
		t.Fatalf("get by week: %v", err)
	}
	if byWeek == nil || byWeek.ID != second.ID {
		t.Fatalf("plan by week = %+v, want id %d", byWeek, second.ID)
	}

	// The canceled plan is still reachable by id, for archival viewing.
	archived, err := store.GetPlanByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if archived == nil || !archived.Canceled {
		t.Fatalf("archived plan = %+v, want canceled=true", archived)
	}

	// And it still shows up in the full history listing.
	all, err := store.ListPlans(ctx, hh.ID)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d plans in history, want 2 (canceled one kept)", len(all))
	}
}

func TestSetPlanDayStatus(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, _ := seedScalableDay(t, store, hh.ID)

	// A day with no row yet still takes a status.
	if err := store.SetPlanDayStatus(ctx, planID, "2026-01-08", db.DayEatingOut); err != nil {
		t.Fatalf("set status on a fresh day: %v", err)
	}
	day, _ := store.GetPlanDay(ctx, planID, "2026-01-08")
	if day == nil || day.Status != db.DayEatingOut {
		t.Fatalf("status = %+v, want eating_out", day)
	}

	if err := store.SetPlanDayStatus(ctx, planID, "2026-01-08", "brunching"); err == nil {
		t.Error("an unknown status was accepted")
	}

	// A day row written by the ordinary upsert path defaults to cooking, not
	// to an empty string - the plan page switches on this value.
	if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID: planID, Date: "2026-01-09", Headcount: 2,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	fresh, _ := store.GetPlanDay(ctx, planID, "2026-01-09")
	if fresh == nil || fresh.Status != db.DayCooking {
		t.Errorf("upserted day status = %+v, want cooking", fresh)
	}
}

// A headcount change must not quietly un-mark a day the user marked eating
// out: UpsertPlanDay runs on generation and on every people-picker save, and a
// status carried on its params would default to "cooking" at each of them.
func TestUpsertPlanDayPreservesStatus(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, _ := seedScalableDay(t, store, hh.ID)

	if err := store.SetPlanDayStatus(ctx, planID, "2026-01-05", db.DaySkipped); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID: planID, Date: "2026-01-05", Headcount: 5,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	day, _ := store.GetPlanDay(ctx, planID, "2026-01-05")
	if day == nil || day.Status != db.DaySkipped {
		t.Errorf("status = %+v, want it to survive a headcount change", day)
	}
	if day.Headcount != 5 {
		t.Errorf("headcount = %d, want 5", day.Headcount)
	}
}

// A day marked eating out or skipped contributes nothing to the shopping list.
// The filter lives in ListIngredientsByPlan because every list-building path
// reads through it.
func TestListIngredientsByPlanSkipsNonCookingDays(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, _ := seedScalableDay(t, store, hh.ID)

	before, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("seed day has no ingredients to test with")
	}

	if err := store.SetPlanDayStatus(ctx, planID, "2026-01-05", db.DayEatingOut); err != nil {
		t.Fatalf("set status: %v", err)
	}
	after, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("got %d ingredients from an eating-out day, want 0", len(after))
	}

	// Switching back restores them - nothing was destroyed.
	if err := store.SetPlanDayStatus(ctx, planID, "2026-01-05", db.DayCooking); err != nil {
		t.Fatalf("set back: %v", err)
	}
	restored, _ := store.ListIngredientsByPlan(ctx, planID)
	if len(restored) != len(before) {
		t.Errorf("got %d ingredients back, want %d", len(restored), len(before))
	}
}

// A leftover meal eats an earlier meal's surplus, so nothing is bought for it -
// even when the model listed pseudo-ingredients like "chili mix" for it.
func TestListIngredientsByPlanSkipsLeftoverMeals(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, srcID := seedScalableDay(t, store, hh.ID)

	before, _ := store.ListIngredientsByPlan(ctx, planID)

	m, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-06", Slot: "lunch", Title: "Chili (leftovers)",
		Effort: "quick", Servings: 1, CookedPortions: 1,
	})
	if err != nil {
		t.Fatalf("create meal: %v", err)
	}
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: m.ID, Name: "chili mix", Quantity: 1, Unit: "each",
	}); err != nil {
		t.Fatalf("create ingredient: %v", err)
	}
	if err := store.UpdateMealLeftover(ctx, m.ID, true, &srcID); err != nil {
		t.Fatalf("mark leftover: %v", err)
	}

	after, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("got %d ingredients, want %d (leftover meal's lines excluded)", len(after), len(before))
	}
}

// Leftovers-titled catalog recipes are not recipes to cook: search never
// returns them and ExcludeLeftoverRecipes drops them, case-insensitively.
func TestLeftoverRecipesExcluded(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	for _, title := range []string{"Weeknight Chili", "Chili (Leftovers)", "LEFTOVER Chili Bowls"} {
		if _, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
			HouseholdID: hh.ID, Title: title, SourceKind: "ai",
		}); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
	}

	found, err := store.SearchCatalogRecipes(ctx, hh.ID, "chili")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 || found[0].Title != "Weeknight Chili" {
		t.Errorf("search = %+v, want only Weeknight Chili", found)
	}

	all, _ := store.ListCatalogRecipes(ctx, hh.ID)
	if kept := db.ExcludeLeftoverRecipes(all); len(kept) != 1 || kept[0].Title != "Weeknight Chili" {
		t.Errorf("ExcludeLeftoverRecipes = %+v, want only Weeknight Chili", kept)
	}
}
