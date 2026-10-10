package plan

import (
	"context"
	"fmt"
	"log"

	"goeat/db"
)

// SyncResult reports what an edited recipe was carried onto.
type SyncResult struct {
	Meals int
	// PlanIDs are the plans those meals sit on, each once - the plans whose
	// shopping lists the caller now has to refresh.
	PlanIDs []int64
}

// SyncRecipeMeals carries a saved recipe's current title, steps and
// ingredients onto every meal made from it, on the household's active plans
// ending on or after fromDate (YYYY-MM-DD).
//
// A meal is a scaled copy of its recipe (MaterializeRecipe), so without this
// an edit on the recipe page changed the recipe and nothing that cooks it.
// Plans that are already over are left as they were cooked and shopped.
//
// Each meal is rewritten in place rather than deleted and materialised again:
// its slot, status, lock and the leftover meals pointing at it all hang off
// the row and must survive. Its servings and cooked portions are the day's,
// not the recipe's, so they stay; only what the recipe owns is replaced.
//
// The recipe wins over anything edited on the meal itself.
func SyncRecipeMeals(ctx context.Context, store db.Store, recipeID int64, fromDate string) (*SyncResult, error) {
	recipe, err := store.GetCatalogRecipe(ctx, recipeID)
	if err != nil {
		return nil, fmt.Errorf("get recipe: %w", err)
	}
	if recipe == nil {
		return nil, fmt.Errorf("recipe %d not found", recipeID)
	}
	meals, err := store.ListMealsByCatalogRecipe(ctx, recipeID, fromDate)
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}

	res := &SyncResult{}
	seen := map[int64]bool{}
	for _, m := range meals {
		if err := syncMeal(ctx, store, recipe, m); err != nil {
			return res, fmt.Errorf("meal %d: %w", m.ID, err)
		}
		res.Meals++
		if !seen[m.PlanID] {
			seen[m.PlanID] = true
			res.PlanIDs = append(res.PlanIDs, m.PlanID)
		}
	}
	return res, nil
}

func syncMeal(ctx context.Context, store db.Store, recipe *db.CatalogRecipe, m *db.Meal) error {
	if err := store.UpdateMealTitle(ctx, m.ID, recipe.Title, effortFor(recipe), m.Servings, m.CookedPortions); err != nil {
		return fmt.Errorf("update meal: %w", err)
	}
	if recipe.Title != m.Title {
		// Keep the leftovers of this meal named after it, as SwapMeal does.
		deps, err := store.ListLeftoversSourcedFromMeal(ctx, m.ID)
		if err != nil {
			return fmt.Errorf("list leftovers: %w", err)
		}
		for _, d := range deps {
			if err := store.UpdateMealTitle(ctx, d.ID, "Leftover "+recipe.Title, d.Effort, d.Servings, d.CookedPortions); err != nil {
				log.Printf("sync recipe: retitle leftover meal %d: %v", d.ID, err)
			}
		}
	}

	if err := copySteps(ctx, store, recipe.ID, m.ID, m.Servings); err != nil {
		// As in MaterializeRecipe: a meal without its steps still shops.
		log.Printf("sync recipe: copy steps for meal %d: %v", m.ID, err)
	}

	if err := store.DeleteMealIngredients(ctx, m.ID); err != nil {
		return fmt.Errorf("clear ingredients: %w", err)
	}
	if _, _, err := writeIngredients(ctx, store, recipe.HouseholdID, recipe.ID, m.ID, syncFactor(recipe, m)); err != nil {
		return err
	}
	// The fresh rows are written at the meal's current scale, so that is the
	// baseline the next headcount change rescales from.
	if err := store.SetMealBaseline(ctx, m.ID); err != nil {
		log.Printf("sync recipe: set baseline for meal %d: %v", m.ID, err)
	}
	return nil
}

// syncFactor is what to multiply each recipe quantity by so the meal keeps
// cooking what it cooks now: its cooked portions over the recipe's own yield.
//
// Whole portions, as printed on the meal card, and deliberately not the day's
// fractional portion total (two people at 1.0 and 0.8 are 1.8): a card that
// says "2 srv" over a 2-serving recipe has to list that recipe as written,
// not 2.7 eggs. This is also how a generated meal is stored. The fractional
// total still applies when who is eating changes (ScaleMealsForDay).
func syncFactor(recipe *db.CatalogRecipe, m *db.Meal) float64 {
	if recipe.Servings < 1 {
		// No stated yield: taken as written, as scaleFor does.
		return 1
	}
	cooked := m.CookedPortions
	if cooked < m.Servings {
		cooked = m.Servings
	}
	if cooked < 1 {
		return 1
	}
	return float64(cooked) / float64(recipe.Servings)
}
