package plan

import (
	"context"
	"math"
	"testing"

	"goeat/db"
	"goeat/recipes"
)

// An edit to a saved recipe reaches the meal made from it: new title, new
// steps, ingredients added, removed and re-quantified - all at the meal's own
// scale, on the same meal row.
func TestSyncRecipeMealsCarriesAnEditOntoThePlan(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)

	// A 4-serving recipe on a day feeding 2: everything x0.5.
	res, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "dinner", Portions: 2,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := store.SetMealStatus(ctx, res.MealID, db.DaySkipped); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if err := store.UpdateCatalogRecipe(ctx, db.UpdateCatalogRecipeParams{
		ID: recipeID, Title: "Smoky Chili", Servings: 4, PrepMinutes: 5, CookMinutes: 5,
	}); err != nil {
		t.Fatalf("update recipe: %v", err)
	}
	if err := recipes.ReplaceContent(ctx, store, recipeID,
		[]string{"3 lb ground beef", "2 cup black beans"},
		[]string{"Brown the beef.", "Add the beans.", "Simmer."}); err != nil {
		t.Fatalf("replace content: %v", err)
	}

	out, err := SyncRecipeMeals(ctx, store, recipeID, "2026-01-01")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if out.Meals != 1 || len(out.PlanIDs) != 1 || out.PlanIDs[0] != planID {
		t.Fatalf("result = %+v, want 1 meal on plan %d", out, planID)
	}

	meal, _ := store.GetMealByID(ctx, res.MealID)
	if meal == nil {
		t.Fatal("the meal row was replaced, not updated")
	}
	if meal.Title != "Smoky Chili" || meal.Effort != "quick" {
		t.Errorf("meal = %q / %s, want Smoky Chili / quick", meal.Title, meal.Effort)
	}
	if meal.Servings != 2 || meal.Status != db.DaySkipped {
		t.Errorf("servings %d status %s: the meal's own state did not survive", meal.Servings, meal.Status)
	}

	ing := ingredientsByName(t, store, res.MealID)
	if len(ing) != 2 {
		t.Fatalf("ingredients = %d, want 2", len(ing))
	}
	if got := ing["ground beef"].Quantity; math.Abs(got-1.5) > 1e-9 {
		t.Errorf("ground beef = %v, want 1.5", got)
	}
	if got := ing["black beans"]; got == nil || math.Abs(got.Quantity-1) > 1e-9 {
		t.Errorf("black beans = %+v, want 1", got)
	}
	if _, ok := ing["kidney beans"]; ok {
		t.Error("an ingredient removed from the recipe is still on the meal")
	}

	mr, _ := store.GetMealRecipe(ctx, res.MealID)
	if mr == nil || mr.StepsJSON != `["Brown the beef.","Add the beans.","Simmer."]` {
		t.Errorf("steps = %+v", mr)
	}

	// The new amounts are the rescale baseline: 2 -> 4 portions doubles them.
	if _, err := store.ScaleMealsForDay(ctx, planID, "2026-01-05", 4); err != nil {
		t.Fatalf("scale: %v", err)
	}
	if got := ingredientsByName(t, store, res.MealID)["ground beef"].Quantity; math.Abs(got-3) > 1e-9 {
		t.Errorf("ground beef after rescale = %v, want 3", got)
	}
}

// A meal cooking extra for a later leftover meal keeps cooking that extra,
// and the leftover meal follows a renamed recipe.
func TestSyncRecipeMealsKeepsLeftoverPortions(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)

	src, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "dinner", Portions: 2,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	lo, err := AddLeftoverMeal(ctx, store, LeftoverParams{
		PlanID: planID, SourceMealID: src.MealID, Date: "2026-01-06", Slot: "lunch", Portions: 2,
	})
	if err != nil {
		t.Fatalf("leftover: %v", err)
	}

	if err := store.UpdateCatalogRecipe(ctx, db.UpdateCatalogRecipeParams{
		ID: recipeID, Title: "Smoky Chili", Servings: 4, PrepMinutes: 10, CookMinutes: 40,
	}); err != nil {
		t.Fatalf("update recipe: %v", err)
	}
	if _, err := SyncRecipeMeals(ctx, store, recipeID, "2026-01-01"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// 2 eating + 2 for leftovers = the recipe's full 4 servings.
	if got := ingredientsByName(t, store, src.MealID)["ground beef"].Quantity; math.Abs(got-2) > 1e-9 {
		t.Errorf("ground beef = %v, want 2", got)
	}
	if m, _ := store.GetMealByID(ctx, lo.MealID); m == nil || m.Title != "Leftover Smoky Chili" {
		t.Errorf("leftover meal = %+v, want title Leftover Smoky Chili", m)
	}
}

// Plans that are over, and meals from another recipe, are left alone.
func TestSyncRecipeMealsLeavesOtherMealsAlone(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)

	res, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "dinner", Portions: 2,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	other, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-06", Slot: "dinner", Title: "Tacos",
		Effort: "quick", Servings: 2, CookedPortions: 2,
	})
	if err != nil {
		t.Fatalf("create meal: %v", err)
	}
	if err := recipes.ReplaceContent(ctx, store, recipeID, []string{"9 lb ground beef"}, nil); err != nil {
		t.Fatalf("replace content: %v", err)
	}

	// The plan ended 2026-01-10.
	out, err := SyncRecipeMeals(ctx, store, recipeID, "2026-01-11")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if out.Meals != 0 {
		t.Errorf("synced %d meals on a finished plan, want 0", out.Meals)
	}
	if got := ingredientsByName(t, store, res.MealID)["ground beef"].Quantity; math.Abs(got-1) > 1e-9 {
		t.Errorf("ground beef on a finished plan = %v, want the original 1", got)
	}

	if _, err := SyncRecipeMeals(ctx, store, recipeID, "2026-01-01"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if m, _ := store.GetMealByID(ctx, other.ID); m == nil || m.Title != "Tacos" {
		t.Errorf("unlinked meal = %+v, want it untouched", m)
	}
}

// A "2 srv" meal over a 2-serving recipe lists the recipe as written, even on
// a day whose portion total is fractional (two people at 1.0 and 0.8).
func TestSyncRecipeMealsUsesWholeServings(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)
	if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID: planID, Date: "2026-01-05", Headcount: 2, Portions: 1.8,
	}); err != nil {
		t.Fatalf("plan day: %v", err)
	}
	res, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "breakfast", Portions: 1.8,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := store.UpdateCatalogRecipe(ctx, db.UpdateCatalogRecipeParams{
		ID: recipeID, Title: "Weeknight Chili", Servings: 2,
	}); err != nil {
		t.Fatalf("update recipe: %v", err)
	}
	if err := recipes.ReplaceContent(ctx, store, recipeID, []string{"3 large eggs", "2 slice bread"}, nil); err != nil {
		t.Fatalf("replace content: %v", err)
	}
	if _, err := SyncRecipeMeals(ctx, store, recipeID, "2026-01-01"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	ing := ingredientsByName(t, store, res.MealID)
	if got := ing["eggs"]; got == nil || got.Quantity != 3 {
		t.Errorf("eggs = %+v, want 3", got)
	}
	if got := ing["bread"]; got == nil || got.Quantity != 2 {
		t.Errorf("bread = %+v, want 2", got)
	}
}
