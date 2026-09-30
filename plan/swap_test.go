package plan

import (
	"context"
	"math"
	"testing"

	"goeat/db"
)

// seedSourceWithLeftover writes Monday dinner cooking 4 portions for 2, and
// Tuesday lunch (2 servings) eating its leftovers.
func seedSourceWithLeftover(t *testing.T, store db.Store, planID int64) (sourceID, leftoverID int64) {
	t.Helper()
	ctx := context.Background()
	src, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-05", Slot: "dinner", Title: "Roast Chicken",
		Effort: "standard", Servings: 2, CookedPortions: 4,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	lo, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-06", Slot: "lunch", Title: "Leftover Roast Chicken",
		Effort: "quick", Servings: 2, CookedPortions: 2,
	})
	if err != nil {
		t.Fatalf("create leftover: %v", err)
	}
	if err := store.UpdateMealLeftover(ctx, lo.ID, true, &src.ID); err != nil {
		t.Fatalf("mark leftover: %v", err)
	}
	return src.ID, lo.ID
}

func TestSwapMealResolutions(t *testing.T) {
	ctx := context.Background()

	t.Run("carry cooks extra and relinks the leftover", func(t *testing.T) {
		store, hh, planID := newMatStore(t)
		recipeID := seedRecipe(t, store, hh.ID)
		srcID, loID := seedSourceWithLeftover(t, store, planID)

		res, err := SwapMeal(ctx, store, SwapParams{
			MealID: srcID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
			Portions: 2, Resolution: SwapCarry,
		})
		if err != nil {
			t.Fatalf("swap: %v", err)
		}
		if res.OldTitle != "Roast Chicken" || res.Resolved != 1 {
			t.Errorf("old title %q resolved %d, want Roast Chicken / 1", res.OldTitle, res.Resolved)
		}
		if old, _ := store.GetMealByID(ctx, srcID); old != nil {
			t.Error("the swapped-out meal is still there")
		}

		meal, _ := store.GetMealByID(ctx, res.MealID)
		if meal.Day != "2026-01-05" || meal.Slot != "dinner" {
			t.Errorf("new meal landed on %s %s", meal.Day, meal.Slot)
		}
		if meal.Servings != 2 || meal.CookedPortions != 4 {
			t.Errorf("servings/cooked = %d/%d, want 2/4", meal.Servings, meal.CookedPortions)
		}
		// 4-serving recipe cooked for 4 portions: as written.
		if got := ingredientsByName(t, store, res.MealID)["ground beef"].Quantity; math.Abs(got-2) > 1e-9 {
			t.Errorf("ground beef = %v, want 2", got)
		}

		lo, _ := store.GetMealByID(ctx, loID)
		if !lo.IsLeftover || lo.LeftoverSourceMealID == nil || *lo.LeftoverSourceMealID != res.MealID {
			t.Errorf("leftover not relinked to the new meal: %+v", lo)
		}
		if lo.Title != "Leftover Weeknight Chili" {
			t.Errorf("leftover title = %q", lo.Title)
		}
	})

	t.Run("replace clears the leftover and names its slot", func(t *testing.T) {
		store, hh, planID := newMatStore(t)
		recipeID := seedRecipe(t, store, hh.ID)
		srcID, loID := seedSourceWithLeftover(t, store, planID)

		res, err := SwapMeal(ctx, store, SwapParams{
			MealID: srcID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
			Portions: 2, Resolution: SwapReplace,
		})
		if err != nil {
			t.Fatalf("swap: %v", err)
		}
		if lo, _ := store.GetMealByID(ctx, loID); lo != nil {
			t.Error("the leftover meal was not cleared")
		}
		if res.FillDate != "2026-01-06" || res.FillSlot != "lunch" {
			t.Errorf("fill = %s %s, want 2026-01-06 lunch", res.FillDate, res.FillSlot)
		}
		meal, _ := store.GetMealByID(ctx, res.MealID)
		if meal.CookedPortions != 2 {
			t.Errorf("cooked = %d, want 2 (nothing left to cook extra for)", meal.CookedPortions)
		}
	})

	t.Run("ignore keeps the leftover flagged with no source", func(t *testing.T) {
		store, hh, planID := newMatStore(t)
		recipeID := seedRecipe(t, store, hh.ID)
		srcID, loID := seedSourceWithLeftover(t, store, planID)

		if _, err := SwapMeal(ctx, store, SwapParams{
			MealID: srcID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
			Portions: 2, Resolution: SwapIgnore,
		}); err != nil {
			t.Fatalf("swap: %v", err)
		}
		lo, _ := store.GetMealByID(ctx, loID)
		if lo == nil || !lo.IsLeftover || lo.LeftoverSourceMealID != nil {
			t.Errorf("leftover = %+v, want flagged with no source", lo)
		}
		if lo.Title != "Leftover Roast Chicken" {
			t.Errorf("ignore renamed the leftover to %q", lo.Title)
		}
	})
}
