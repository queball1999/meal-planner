package plan

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"goeat/db"
)

// DefaultNovelty is the generate dialog's slider when nobody has moved it:
// an even chance, per meal, of a saved recipe or a new dish.
const DefaultNovelty = 0.5

// maxListedRecipes caps how many saved recipes go into the prompt. A household
// with hundreds should not spend the context window on a list the model will
// not read in full; the rest are still reachable through search_recipes.
const maxListedRecipes = 60

// RecipeMix is how the meals the household did not ask for by name are split
// between its saved recipes and new dishes (the dialog's "stick to what I
// know" / "try something new" slider), with the saved recipes on offer.
type RecipeMix struct {
	Saved int // meals to take from Recipes
	New   int // meals to write from scratch

	// Recipes are the saved recipes offered to the model, each usable once.
	// Empty when Saved is 0.
	Recipes []*db.CatalogRecipe
	// More is true when the household has usable recipes beyond Recipes.
	More bool
}

// rollRecipeMix decides, one meal at a time, whether each of the slots free
// meals is a saved recipe or a new dish: roll() < novelty makes it new. So
// novelty 0 is all saved recipes, 1 is all new, and anything between varies
// from week to week around that share.
//
// The roll happens here rather than in the prompt because a model asked to
// "pick a saved recipe 30% of the time" does not flip a coin - it does
// whatever it was going to do. It is given the two counts instead.
//
// A saved recipe is used at most once a week, so Saved is capped at how many
// there are; the overflow becomes new dishes. Returns nil when there is
// nothing to decide: no free slots, or no saved recipes to choose from.
func rollRecipeMix(recipes []*db.CatalogRecipe, slots int, novelty float64, roll func() float64) *RecipeMix {
	saved := 0
	for i := 0; i < slots; i++ {
		if roll() >= novelty {
			saved++
		}
	}
	return fixedRecipeMix(recipes, slots, saved)
}

// fixedRecipeMix is the mix for a number of saved-recipe meals already
// decided - rolled just now, or rolled earlier for the dialog's review step
// (PreviewRecipeMix) and sent back with the form so the plan is what the
// review said it would be. saved is held to what is possible.
func fixedRecipeMix(recipes []*db.CatalogRecipe, slots, saved int) *RecipeMix {
	if slots <= 0 || len(recipes) == 0 {
		return nil
	}
	saved = max(0, min(saved, slots, len(recipes)))
	mix := &RecipeMix{Saved: saved, New: slots - saved}
	if mix.Saved == 0 {
		return mix
	}

	// A different sample each week when there are too many to list, so the
	// same sixty are not the only ones that ever get cooked.
	offered := append([]*db.CatalogRecipe(nil), recipes...)
	if len(offered) > maxListedRecipes {
		rand.Shuffle(len(offered), func(i, j int) { offered[i], offered[j] = offered[j], offered[i] })
		offered = offered[:maxListedRecipes]
		mix.More = true
	}
	mix.Recipes = offered
	return mix
}

// MixPreview is what the generate dialog's review step says about the meals
// the household did not ask for by name.
type MixPreview struct {
	Free      int // meals left for the plan to decide; 0 when every one was asked for
	Saved     int // of those, how many come from saved recipes
	New       int // and how many are new dishes
	Available int // saved recipes the plan could use
	// Capped is true when the roll wanted more saved recipes than there are.
	Capped bool
}

// PreviewRecipeMix rolls the saved/new split for a plan of slots meals of
// which requested were asked for by name, without generating anything. The
// dialog shows the result and posts Saved back (db.PlanRequest.SavedMeals),
// so the roll shown is the roll used.
func PreviewRecipeMix(ctx context.Context, store db.Store, householdID int64, slots, requested int, picked map[int64]bool, novelty float64) (MixPreview, error) {
	p := MixPreview{Free: max(0, slots-requested)}
	if p.Free == 0 {
		return p, nil
	}
	profile, err := Resolve(ctx, store, householdID)
	if err != nil {
		return p, fmt.Errorf("resolve preferences: %w", err)
	}
	recipes, err := usableRecipes(ctx, store, householdID, profile, picked)
	if err != nil {
		return p, err
	}
	p.Available = len(recipes)
	wanted := 0
	for i := 0; i < p.Free; i++ {
		if rand.Float64() >= novelty {
			wanted++
		}
	}
	p.Saved = min(wanted, p.Available)
	p.New = p.Free - p.Saved
	p.Capped = wanted > p.Available
	return p, nil
}

// usableRecipes returns the household's saved recipes a generated plan may
// use: not leftovers entries, not ones already picked on the dialog (exclude -
// those go in regardless), not ones the household disliked, and none with an
// ingredient its allergies or diet forbid - Validate would throw the whole
// plan out over it.
func usableRecipes(ctx context.Context, store db.Store, householdID int64, profile *PreferenceProfile, exclude map[int64]bool) ([]*db.CatalogRecipe, error) {
	all, err := store.ListCatalogRecipes(ctx, householdID)
	if err != nil {
		return nil, err
	}
	disliked := toLowerSet(profile.FeedbackDisliked)
	var out []*db.CatalogRecipe
	for _, rc := range db.ExcludeLeftoverRecipes(all) {
		if exclude[rc.ID] || disliked[strings.ToLower(strings.TrimSpace(rc.Title))] {
			continue
		}
		if ok, err := recipeAllowed(ctx, store, rc, profile); err != nil {
			return nil, err
		} else if ok {
			out = append(out, rc)
		}
	}
	return out, nil
}

// recipeAllowed reports whether a saved recipe can go into a generated plan
// as it stands: it has ingredients, and none breaks an allergy or diet rule.
func recipeAllowed(ctx context.Context, store db.Store, rc *db.CatalogRecipe, profile *PreferenceProfile) (bool, error) {
	lines, err := store.ListCatalogRecipeIngredients(ctx, rc.ID)
	if err != nil {
		return false, err
	}
	if len(lines) == 0 {
		return false, nil
	}
	allergySet := toLowerSet(profile.Allergies)
	dietRules := dietIngredientRules(profile.DietTags)
	for _, ln := range lines {
		name := strings.ToLower(ln.Name)
		for allergen := range allergySet {
			if allergen != "" && strings.Contains(name, allergen) {
				return false, nil
			}
		}
		for _, rule := range dietRules {
			if rule(name) {
				return false, nil
			}
		}
	}
	return true, nil
}

// writeRecipeMix adds the saved-recipes section to the generation prompt.
// requested is how many meals the household asked for by name: those go in
// whatever the mix says, and the mix covers only the meals left over.
func writeRecipeMix(b *strings.Builder, mix *RecipeMix, requested int) {
	total := mix.Saved + mix.New
	which := "meals this week"
	if requested > 0 {
		which = "meals not already requested above"
	}
	switch {
	case mix.Saved == 0:
		fmt.Fprintf(b, "\nSaved recipes: the household wants to try something new. All %d %s must be new dishes - do not use a saved recipe for them.\n", total, which)
		return
	case mix.New == 0:
		fmt.Fprintf(b, "\nSaved recipes: the household wants to stick to what it knows. All %d %s must come from its saved recipes below - do not invent a dish. Write a new one only for a slot that no saved recipe fits at all (nothing suitable for breakfast, say).\n", total, which)
	default:
		fmt.Fprintf(b, "\nSaved recipes: of the %d %s, %d must come from the household's saved recipes below and the other %d must be new dishes it has not saved. Fall short on saved recipes only where none of them fits a slot at all.\n", total, which, mix.Saved, mix.New)
	}
	b.WriteString("A leftovers meal counts as neither. Look through this list before deciding to write anything new, and use each recipe at most once. To use one, give the meal its recipe_id:\n")
	for _, rc := range mix.Recipes {
		fmt.Fprintf(b, "- [%d] %s", rc.ID, rc.Title)
		var bits []string
		if len(rc.Tags) > 0 {
			bits = append(bits, strings.Join(rc.Tags, ", "))
		}
		if mins := rc.PrepMinutes + rc.CookMinutes; mins > 0 {
			bits = append(bits, fmt.Sprintf("%d min", mins))
		}
		if len(bits) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(bits, "; "))
		}
		b.WriteString("\n")
	}
	if mix.More {
		b.WriteString("The household has more saved recipes than fit here - search_recipes finds the rest.\n")
	}
}

// expandSavedRecipes fills in every meal the model wrote as a reference to a
// saved recipe ({"day", "slot", "recipe_id"}) with that recipe as saved: its
// title, ingredients scaled to what the meal cooks, steps, times and tags.
// servings is the household's, for a meal that did not state its own.
//
// Copying the recipe here, instead of asking the model to write it out again,
// is what makes "reuse" mean the recipe the household saved and not a
// reinvention under the same title - and it costs no output tokens.
func expandSavedRecipes(ctx context.Context, store db.Store, householdID int64, meals []GeneratedMeal, servings int) error {
	for i := range meals {
		gm := &meals[i]
		if gm.RecipeID == 0 {
			continue
		}
		recipe, err := store.GetCatalogRecipe(ctx, gm.RecipeID)
		if err != nil {
			return fmt.Errorf("get recipe %d: %w", gm.RecipeID, err)
		}
		if recipe == nil || recipe.HouseholdID != householdID || db.IsLeftoverTitle(recipe.Title) {
			// An id that is not a recipe of this household. A meal written out
			// in full anyway is kept as a new dish; a bare reference is nothing.
			if len(gm.Ingredients) > 0 {
				gm.RecipeID = 0
				continue
			}
			return fmt.Errorf("%s %s names saved recipe %d, which does not exist", gm.Day, gm.Slot, gm.RecipeID)
		}
		lines, err := store.ListCatalogRecipeIngredients(ctx, recipe.ID)
		if err != nil {
			return fmt.Errorf("list ingredients of recipe %d: %w", recipe.ID, err)
		}
		steps, err := store.ListCatalogRecipeSteps(ctx, recipe.ID)
		if err != nil {
			return fmt.Errorf("list steps of recipe %d: %w", recipe.ID, err)
		}

		gm.Title = recipe.Title
		if gm.Servings < 1 {
			gm.Servings = servings
		}
		if gm.CookedPortions < gm.Servings {
			gm.CookedPortions = gm.Servings
		}
		if !validEfforts[strings.ToLower(gm.Effort)] {
			gm.Effort = effortFor(recipe)
		}
		gm.PrepMinutes, gm.CookMinutes, gm.Tags = recipe.PrepMinutes, recipe.CookMinutes, recipe.Tags

		// The quantities cover everything the meal cooks (see
		// ensureGeneratedRecipe). A recipe with no stated yield is taken as
		// written - guessing a denominator would multiply every quantity.
		factor := 1.0
		if recipe.Servings >= 1 {
			factor = float64(gm.CookedPortions) / float64(recipe.Servings)
		}
		gm.Ingredients = make([]GeneratedIngredient, 0, len(lines))
		for _, ln := range lines {
			qty, _ := ParseQuantity(ln.Quantity)
			unit := strings.TrimSpace(ln.Unit)
			if unit == "" {
				// An imported "2 eggs" has no unit; a count is what it means.
				unit = "each"
			}
			gm.Ingredients = append(gm.Ingredients, GeneratedIngredient{
				Name: ln.Name, Quantity: round3(qty * factor), Unit: unit,
			})
		}
		gm.Steps = make([]string, 0, len(steps))
		for _, s := range steps {
			gm.Steps = append(gm.Steps, s.Text)
		}
	}
	return nil
}
