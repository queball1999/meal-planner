package plan

import "testing"

func TestClampQuantities(t *testing.T) {
	gp := GeneratedPlan{Meals: []GeneratedMeal{
		{
			Title:    "Absurd Dinner",
			Servings: 4,
			Ingredients: []GeneratedIngredient{
				{Name: "chicken breast", Quantity: 681, Unit: "lb"},
				{Name: "eggs", Quantity: 108, Unit: "each"},
				{Name: "olive oil", Quantity: 2, Unit: "tbsp"},
				{Name: "saffron", Quantity: 0.001, Unit: "kg"},
			},
		},
		{
			Title:    "Big Batch",
			Servings: 20, // caps scale up 5x
			Ingredients: []GeneratedIngredient{
				{Name: "flour", Quantity: 15000, Unit: "g"}, // 4000*5 = 20000 cap -> untouched
				{Name: "flour", Quantity: 25000, Unit: "g"}, // over the scaled cap -> clamped
			},
		},
	}}

	notes := ClampQuantities(gp)
	if len(notes) != 3 {
		t.Fatalf("got %d clamp notes, want 3: %v", len(notes), notes)
	}

	m0 := gp.Meals[0].Ingredients
	if m0[0].Quantity != 9 {
		t.Errorf("chicken lb = %v, want 9 (the per-line cap)", m0[0].Quantity)
	}
	if m0[1].Quantity != 48 {
		t.Errorf("eggs each = %v, want 48", m0[1].Quantity)
	}
	if m0[2].Quantity != 2 {
		t.Errorf("olive oil tbsp = %v, want 2 (untouched)", m0[2].Quantity)
	}
	if m0[3].Quantity != 0.001 {
		t.Errorf("saffron kg = %v, want 0.001 (untouched)", m0[3].Quantity)
	}

	m1 := gp.Meals[1].Ingredients
	if m1[0].Quantity != 15000 {
		t.Errorf("flour 15000 g at 20 servings = %v, want 15000 (under scaled cap)", m1[0].Quantity)
	}
	if m1[1].Quantity != 20000 {
		t.Errorf("flour 25000 g at 20 servings = %v, want 20000 (scaled cap 4000*5)", m1[1].Quantity)
	}
}
