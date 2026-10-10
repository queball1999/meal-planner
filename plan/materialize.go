package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"

	"goeat/catalog"
	"goeat/db"
	"goeat/pricing"
)

// MaterializeParams says which saved recipe goes into which slot.
type MaterializeParams struct {
	PlanID          int64
	HouseholdID     int64
	CatalogRecipeID int64
	Date            string // YYYY-MM-DD
	Slot            string // "breakfast" | "lunch" | "dinner"

	// Portions is what the day is scaled to - the sum of the eating members'
	// portion factors (see db.PlanDay.Portions). Zero means "use the recipe's
	// own yield", which is what a caller with no day context wants.
	Portions float64

	// ExtraPortions is cooked on top of Portions for later meals that eat
	// this one's leftovers (a swap keeping its leftovers fed). The meal's
	// servings stay at Portions; cooked_portions and every ingredient cover
	// both.
	ExtraPortions float64
}

// MaterializeResult reports what was written, so a caller can tell the user
// what happened without re-reading it.
type MaterializeResult struct {
	MealID      int64
	Title       string
	Servings    int
	Ingredients int
	// Unquantified names the recipe lines whose amount was free text with no
	// number in it ("a pinch", "to taste"). They are still added to the meal -
	// dropping an ingredient silently would be worse - but they cannot be
	// priced or aggregated, so the caller is told which ones.
	Unquantified []string
}

// MaterializeRecipe turns a saved catalog recipe into a real meal on a plan:
// the meals row, its meal_recipes steps, and one meal_ingredient per recipe
// line, scaled from the recipe's own yield to the day's portion total.
//
// This is the engine behind "pick a replacement" in the day-status dialog and
// behind the agent's swap/edit meal tools. It lives in plan/ rather than in a
// handler precisely so those two cannot drift apart.
//
// Any meal already in that slot is replaced. Filling a slot means filling it;
// leaving two meals stacked in one slot would render as whichever the calendar
// query happened to see first.
//
// Scaling writes both the current and the base columns (00017): the base is
// what a later headcount change rescales *from*, and for a meal created at a
// given portion total the as-created amount is that baseline.
func MaterializeRecipe(ctx context.Context, store db.Store, p MaterializeParams) (*MaterializeResult, error) {
	recipe, err := store.GetCatalogRecipe(ctx, p.CatalogRecipeID)
	if err != nil {
		return nil, fmt.Errorf("get recipe: %w", err)
	}
	if recipe == nil || recipe.HouseholdID != p.HouseholdID {
		return nil, fmt.Errorf("recipe %d not found", p.CatalogRecipeID)
	}

	// The recipe's own yield is the denominator for scaling. A recipe with no
	// stated yield is treated as serving what the day needs, i.e. not scaled -
	// guessing a denominator would silently multiply every quantity.
	baseServings := recipe.Servings
	if baseServings < 1 {
		baseServings = 0
	}
	servings, factor := scaleFor(baseServings, p.Portions)
	cooked := servings
	if p.ExtraPortions > 0 {
		cooked, factor = scaleFor(baseServings, p.Portions+p.ExtraPortions)
		if extra := int(math.Round(p.ExtraPortions)); cooked < servings+extra {
			cooked = servings + extra
		}
	}

	if err := replaceSlot(ctx, store, p.PlanID, p.Date, p.Slot); err != nil {
		return nil, err
	}

	meal, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID:         p.PlanID,
		Day:            p.Date,
		Slot:           p.Slot,
		Title:          recipe.Title,
		Effort:         effortFor(recipe),
		Servings:       servings,
		CookedPortions: cooked,
		// The link is what lets a later edit to the recipe reach this meal
		// (SyncRecipeMeals).
		CatalogRecipeID: &recipe.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("create meal: %w", err)
	}

	res := &MaterializeResult{MealID: meal.ID, Title: recipe.Title, Servings: servings}

	if err := copySteps(ctx, store, recipe.ID, meal.ID, servings); err != nil {
		// A meal without its steps is still a meal you can shop for, so this
		// is logged rather than fatal.
		log.Printf("materialize: copy steps for recipe %d: %v", recipe.ID, err)
	}

	// The baseline a later headcount change rescales from is the meal at its
	// whole servings: ScaleMealsForDay multiplies it by portions / servings. A
	// day of 1.8 portions is a 2-serving meal, so the ingredients are written
	// for 2, recorded as the baseline, and only then brought down to 1.8.
	// Recording the 1.8 amounts as the 2-serving baseline would leave the meal
	// 10% short for good, whoever ate on that day afterwards.
	frac := 1.0
	if baseServings >= 1 && p.Portions > 0 {
		frac = p.Portions / float64(servings)
	}
	res.Ingredients, res.Unquantified, err = writeIngredients(ctx, store, p.HouseholdID, recipe.ID, meal.ID, factor/frac)
	if err != nil {
		return nil, err
	}
	if err := store.SetMealBaseline(ctx, meal.ID); err != nil {
		log.Printf("materialize: set baseline for meal %d: %v", meal.ID, err)
	}
	if frac != 1 {
		if err := store.ScaleMealFromBaseline(ctx, meal.ID, frac); err != nil {
			return nil, fmt.Errorf("scale to day portions: %w", err)
		}
	}

	return res, nil
}

// writeIngredients writes one meal_ingredient per recipe line, each scaled by
// factor and linked to a catalog item. It reports how many it wrote and which
// lines had no number in their amount (see MaterializeResult.Unquantified).
func writeIngredients(ctx context.Context, store db.Store, householdID, recipeID, mealID int64, factor float64) (written int, unquantified []string, err error) {
	lines, err := store.ListCatalogRecipeIngredients(ctx, recipeID)
	if err != nil {
		return 0, nil, fmt.Errorf("list recipe ingredients: %w", err)
	}
	for _, ln := range lines {
		qty, ok := ParseQuantity(ln.Quantity)
		if !ok {
			unquantified = append(unquantified, ln.Name)
		}
		qty = round3(qty * factor)

		var itemID *int64
		if it, ierr := catalog.EnsureItem(ctx, store, householdID, ln.Name); ierr == nil && it != nil {
			itemID = &it.ID
		}

		if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID:         mealID,
			Name:           ln.Name,
			Quantity:       qty,
			Unit:           ln.Unit,
			NormalizedTerm: pricing.Normalize(ln.Name),
			ItemID:         itemID,
		}); err != nil {
			return written, unquantified, fmt.Errorf("create ingredient %q: %w", ln.Name, err)
		}
		written++
	}
	return written, unquantified, nil
}

// scaleFor works out the meal's servings and the factor to multiply every
// ingredient by.
func scaleFor(baseServings int, portions float64) (servings int, factor float64) {
	if portions <= 0 || baseServings < 1 {
		// No day context, or a recipe with no stated yield: take the recipe as
		// written. Its own servings stand, or one if it does not say.
		if baseServings < 1 {
			if portions > 0 {
				return int(math.Round(portions)), 1
			}
			return 1, 1
		}
		return baseServings, 1
	}
	servings = int(math.Round(portions))
	if servings < 1 {
		servings = 1
	}
	return servings, portions / float64(baseServings)
}

// replaceSlot clears whatever meal already occupies the slot.
func replaceSlot(ctx context.Context, store db.Store, planID int64, date, slot string) error {
	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil {
		return fmt.Errorf("list meals: %w", err)
	}
	for _, m := range meals {
		if m.Day == date && m.Slot == slot {
			if err := store.DeleteMeal(ctx, m.ID); err != nil {
				return fmt.Errorf("clear slot: %w", err)
			}
		}
	}
	return nil
}

// copySteps writes the recipe's instructions onto the meal, replacing any the
// meal already had.
func copySteps(ctx context.Context, store db.Store, recipeID, mealID int64, servings int) error {
	steps, err := store.ListCatalogRecipeSteps(ctx, recipeID)
	if err != nil {
		return err
	}
	texts := make([]string, 0, len(steps))
	for _, s := range steps {
		texts = append(texts, s.Text)
	}
	blob, err := json.Marshal(texts)
	if err != nil {
		return err
	}
	return store.SetMealSteps(ctx, mealID, string(blob), servings)
}

// effortFor maps a recipe's total time onto the three effort buckets the plan
// UI and the generation prompt already use. A recipe with no times recorded is
// "standard" - the neutral middle, rather than claiming it is quick.
func effortFor(r *db.CatalogRecipe) string {
	total := r.PrepMinutes + r.CookMinutes
	switch {
	case total == 0:
		return "standard"
	case total < 15:
		return "quick"
	case total <= 45:
		return "standard"
	default:
		return "elaborate"
	}
}

// round3 matches the precision ScaleMealsForDay persists at: enough for
// "0.375 cup" without writing float noise into the shopping-list maths.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
