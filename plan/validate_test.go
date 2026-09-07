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
	if err := Validate(gp, &PreferenceProfile{}); err != nil {
		t.Fatalf("Validate rejected a 0-quantity seasoning: %v", err)
	}
}

func TestValidate_NegativeQuantityRejected(t *testing.T) {
	gp := make21Meals()
	gp.Meals[0].Ingredients[0].Quantity = -1
	if err := Validate(gp, &PreferenceProfile{}); err == nil {
		t.Fatal("Validate should reject a negative quantity")
	}
}

func TestValidate_MissingUnitRejected(t *testing.T) {
	gp := make21Meals()
	gp.Meals[0].Ingredients[0].Unit = ""
	if err := Validate(gp, &PreferenceProfile{}); err == nil {
		t.Fatal("Validate should reject an empty unit")
	}
}
