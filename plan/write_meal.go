package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"goeat/db"
)

// WriteMealParams says which slot gets a meal that is not in the recipe
// catalog yet - one the caller has the whole recipe for.
type WriteMealParams struct {
	PlanID      int64
	HouseholdID int64
	Date        string // YYYY-MM-DD
	Slot        string // "breakfast" | "lunch" | "dinner"
	Portions    float64

	// Meal is the recipe as written. Its Servings is the yield the ingredient
	// quantities are written for, and what they are scaled from; Day and Slot
	// are ignored in favour of Date and Slot above.
	Meal GeneratedMeal
}

// WriteMealResult reports what WriteMeal did.
type WriteMealResult struct {
	*MaterializeResult
	RecipeID int64
	// Reused is true when the household already had a recipe by this title:
	// that one was used as it stands and Meal's own content was not saved.
	Reused bool
	// Replaced is the title of the meal that was in the slot, "" if none.
	Replaced string
	// Stranded are the leftover meals that were eating the replaced meal.
	// They are kept, as leftovers of nothing (see SwapIgnore).
	Stranded []*db.Meal
}

// WriteMeal puts a brand-new meal into a slot: it files the recipe in the
// household's catalog, then fills the slot from it exactly as picking a saved
// recipe would, replacing whatever was there. It is what lets the assistant
// act on "Monday lunch is turkey sandwiches" when no saved recipe matches.
//
// Going through the catalog rather than writing meal rows directly keeps one
// path for scaling, item linking and baselines (MaterializeRecipe), and means
// the meal is there to pick again next week - the same reason plan generation
// saves every meal it writes.
func WriteMeal(ctx context.Context, store db.Store, p WriteMealParams) (*WriteMealResult, error) {
	one := []GeneratedMeal{p.Meal}
	CleanMealTitles(GeneratedPlan{Meals: one})
	gm := one[0]
	gm.Title = strings.TrimSpace(gm.Title)

	if gm.Title == "" {
		return nil, errors.New("the meal needs a title")
	}
	if db.IsLeftoverTitle(gm.Title) {
		return nil, errors.New("leftovers are not a recipe - write the meal itself")
	}
	if len(gm.Ingredients) == 0 {
		return nil, errors.New("the meal needs at least one ingredient")
	}
	if gm.Servings < 1 {
		// No stated yield: take the quantities as written for this day.
		gm.Servings = int(p.Portions + 0.5)
		if gm.Servings < 1 {
			gm.Servings = 1
		}
	}

	profile, err := Resolve(ctx, store, p.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("resolve preferences: %w", err)
	}
	if err := validateSingleMeal(gm, profile); err != nil {
		return nil, err
	}

	meals, err := store.ListMealsByPlan(ctx, p.PlanID)
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	var old *db.Meal
	for _, m := range meals {
		if m.Day == p.Date && m.Slot == p.Slot {
			old = m
		}
	}
	if old != nil && old.Locked {
		return nil, ErrMealLocked
	}

	recipeID, created, err := ensureGeneratedRecipe(ctx, store, p.HouseholdID, gm)
	if err != nil {
		return nil, fmt.Errorf("save recipe: %w", err)
	}
	out := &WriteMealResult{RecipeID: recipeID, Reused: !created}

	if old == nil {
		out.MaterializeResult, err = MaterializeRecipe(ctx, store, MaterializeParams{
			PlanID: p.PlanID, HouseholdID: p.HouseholdID, CatalogRecipeID: recipeID,
			Date: p.Date, Slot: p.Slot, Portions: p.Portions,
		})
		if err != nil {
			return nil, err
		}
		return out, nil
	}

	// Read before the swap unhooks them.
	out.Stranded, err = store.ListLeftoversSourcedFromMeal(ctx, old.ID)
	if err != nil {
		return nil, fmt.Errorf("list leftovers: %w", err)
	}
	// SwapIgnore, not the default carry: a different dish was asked for, and
	// relabelling Tuesday's "Leftover Chili" as leftovers of it would invent a
	// meal nobody chose. The caller is told which ones lost their source.
	swapped, err := SwapMeal(ctx, store, SwapParams{
		MealID: old.ID, HouseholdID: p.HouseholdID, CatalogRecipeID: recipeID,
		Portions: p.Portions, Resolution: SwapIgnore,
	})
	if err != nil {
		return nil, err
	}
	out.MaterializeResult = swapped.MaterializeResult
	out.Replaced = old.Title
	return out, nil
}
