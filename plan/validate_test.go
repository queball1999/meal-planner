package plan

import "testing"

// make21Meals returns a schema-valid 21-meal plan (one ingredient each), so a
// test can mutate exactly the ingredient it cares about and still pass every
// other Validate check.
func make21Meals() GeneratedPlan {
	var meals []GeneratedMeal
	for _, day := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			meals = append(meals, GeneratedMeal{
				Day: day, Slot: slot, Title: "Meal " + day + " " + slot,
				Effort: "quick", Servings: 2, CookedPortions: 2,
				Ingredients: []GeneratedIngredient{
					{Name: "rice", Quantity: 1, Unit: "cup", EstPriceCents: 50},
				},
				Steps: []string{"Cook it."},
			})
		}
	}
	return GeneratedPlan{Meals: meals}
}

// TestValidate_ZeroQuantityIngredientIsAllowed reproduces the "salt to taste"
// regression: the LLM routinely expresses a seasoning/garnish as quantity 0,
// and always has. A hard reject there broke otherwise-fine 21-meal plans over
// a single seasoning line - only a negative quantity is a real error.
func TestValidate_ZeroQuantityIngredientIsAllowed(t *testing.T) {
	gp := make21Meals()
	gp.Meals[0].Ingredients = append(gp.Meals[0].Ingredients, GeneratedIngredient{
		Name: "salt", Quantity: 0, Unit: "to taste", EstPriceCents: 1,
	})
	if err := Validate(gp, &PreferenceProfile{}, nil); err != nil {
		t.Fatalf("Validate rejected a 0-quantity seasoning: %v", err)
	}
}

func TestValidate_NegativeQuantityRejected(t *testing.T) {
	gp := make21Meals()
	gp.Meals[0].Ingredients[0].Quantity = -1
	if err := Validate(gp, &PreferenceProfile{}, nil); err == nil {
		t.Fatal("Validate should reject a negative quantity")
	}
}

func TestValidate_MissingUnitRejected(t *testing.T) {
	gp := make21Meals()
	gp.Meals[0].Ingredients[0].Unit = ""
	if err := Validate(gp, &PreferenceProfile{}, nil); err == nil {
		t.Fatal("Validate should reject an empty unit")
	}
}

// make21Meals filtered to a subset of days is what a mid-week "just the
// remaining days" generation returns - Validate must accept it, want exactly
// that many meals, and still reject the days that were left out.
func mealsForDays(days []string) []GeneratedMeal {
	full := make21Meals().Meals
	want := make(map[string]bool, len(days))
	for _, d := range days {
		want[d] = true
	}
	var out []GeneratedMeal
	for _, m := range full {
		if want[m.Day] {
			out = append(out, m)
		}
	}
	return out
}

func TestValidate_AcceptsPartialWeekWhenDaysRestricted(t *testing.T) {
	days := []string{"wednesday", "thursday", "friday", "saturday"}
	gp := GeneratedPlan{Meals: mealsForDays(days)}
	if err := Validate(gp, &PreferenceProfile{}, days); err != nil {
		t.Fatalf("Validate rejected a valid partial-week plan: %v", err)
	}
}

func TestValidate_RejectsFullWeekWhenDaysRestricted(t *testing.T) {
	days := []string{"wednesday", "thursday", "friday", "saturday"}
	gp := make21Meals() // includes sunday-tuesday, which are outside the restricted range
	if err := Validate(gp, &PreferenceProfile{}, days); err == nil {
		t.Fatal("Validate should reject meals outside the requested day range")
	}
}

func TestValidate_RejectsMissingDayWithinRestrictedRange(t *testing.T) {
	days := []string{"wednesday", "thursday", "friday", "saturday"}
	meals := mealsForDays(days)
	meals = meals[3:] // drop wednesday's three meals
	gp := GeneratedPlan{Meals: meals}
	if err := Validate(gp, &PreferenceProfile{}, days); err == nil {
		t.Fatal("Validate should reject a plan missing a day within the requested range")
	}
}
