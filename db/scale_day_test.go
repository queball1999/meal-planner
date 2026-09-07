package db_test

import (
	"context"
	"math"
	"testing"

	"goeat/db"
)

// seedScalableDay creates a plan with one two-serving meal on 2026-01-05 and
// returns the plan and meal ids.
func seedScalableDay(t *testing.T, store db.Store, hhID int64) (planID, mealID int64) {
	t.Helper()
	ctx := context.Background()

	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	m, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID:         p.ID,
		Day:            "2026-01-05",
		Slot:           "dinner",
		Title:          "Chili",
		Effort:         "standard",
		Servings:       2,
		CookedPortions: 3, // deliberate batch-cook surplus
	})
	if err != nil {
		t.Fatalf("create meal: %v", err)
	}
	if err := store.CreateMealRecipe(ctx, db.CreateMealRecipeParams{
		MealID: m.ID, StepsJSON: `["Cook."]`, Servings: 2,
	}); err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	for _, ing := range []struct {
		name string
		qty  float64
		unit string
	}{
		{"ground beef", 1, "lb"},
		{"canned beans", 1.5, "can"},
		{"salt", 0, "tsp"}, // "to taste" lines have no quantity to scale
	} {
		if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID: m.ID, Name: ing.name, Quantity: ing.qty, Unit: ing.unit,
		}); err != nil {
			t.Fatalf("create ingredient %s: %v", ing.name, err)
		}
	}
	return p.ID, m.ID
}

func qtyByName(t *testing.T, store db.Store, mealID int64) map[string]float64 {
	t.Helper()
	ings, err := store.ListIngredientsByMeal(context.Background(), mealID)
	if err != nil {
		t.Fatalf("list ingredients: %v", err)
	}
	out := make(map[string]float64, len(ings))
	for _, ing := range ings {
		out[ing.Name] = ing.Quantity
	}
	return out
}

func TestScaleMealsForDayScalesServingsAndQuantities(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, mealID := seedScalableDay(t, store, hh.ID)

	res, err := store.ScaleMealsForDay(ctx, planID, "2026-01-05", 6)
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	if res.MealsScaled != 1 {
		t.Fatalf("MealsScaled = %d, want 1", res.MealsScaled)
	}

	m, err := store.GetMealByID(ctx, mealID)
	if err != nil {
		t.Fatalf("get meal: %v", err)
	}
	if m.Servings != 6 {
		t.Errorf("servings = %d, want 6", m.Servings)
	}
	if m.BaseServings != 2 {
		t.Errorf("base_servings = %d, want it untouched at 2", m.BaseServings)
	}
	// 3 cooked portions for 2 servings, tripled → 9.
	if m.CookedPortions != 9 {
		t.Errorf("cooked_portions = %d, want 9", m.CookedPortions)
	}

	rec, err := store.GetMealRecipe(ctx, mealID)
	if err != nil || rec == nil {
		t.Fatalf("get recipe: %v", err)
	}
	if rec.Servings != 6 {
		t.Errorf("recipe servings = %d, want 6 (the recipe must state the yield it now makes)", rec.Servings)
	}

	q := qtyByName(t, store, mealID)
	if q["ground beef"] != 3 {
		t.Errorf("ground beef = %v, want 3", q["ground beef"])
	}
	if q["canned beans"] != 4.5 {
		t.Errorf("canned beans = %v, want 4.5", q["canned beans"])
	}
	if q["salt"] != 0 {
		t.Errorf("salt = %v, want 0 (a to-taste line has nothing to scale)", q["salt"])
	}
}

// Scaling reads the as-generated baseline, so a round trip through other
// headcounts must land on exactly the same numbers as going there directly.
func TestScaleMealsForDayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, mealID := seedScalableDay(t, store, hh.ID)

	for _, hc := range []int{6, 3, 7, 1, 3} {
		if _, err := store.ScaleMealsForDay(ctx, planID, "2026-01-05", hc); err != nil {
			t.Fatalf("scale to %d: %v", hc, err)
		}
	}

	m, _ := store.GetMealByID(ctx, mealID)
	if m.Servings != 3 {
		t.Errorf("servings = %d, want 3", m.Servings)
	}
	q := qtyByName(t, store, mealID)
	if math.Abs(q["ground beef"]-1.5) > 1e-9 {
		t.Errorf("ground beef = %v, want exactly 1.5 with no accumulated drift", q["ground beef"])
	}
	if math.Abs(q["canned beans"]-2.25) > 1e-9 {
		t.Errorf("canned beans = %v, want exactly 2.25", q["canned beans"])
	}
}

// Leftover slots carry no ingredients of their own - their portions come from
// the meal that cooked them - so a headcount change must leave them alone.
func TestScaleMealsForDaySkipsLeftovers(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, mealID := seedScalableDay(t, store, hh.ID)

	if err := store.UpdateMealLeftover(ctx, mealID, true, nil); err != nil {
		t.Fatalf("mark leftover: %v", err)
	}
	res, err := store.ScaleMealsForDay(ctx, planID, "2026-01-05", 8)
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	if res.MealsScaled != 0 {
		t.Fatalf("MealsScaled = %d, want 0", res.MealsScaled)
	}
	m, _ := store.GetMealByID(ctx, mealID)
	if m.Servings != 2 {
		t.Errorf("leftover meal servings = %d, want it left at 2", m.Servings)
	}
}

func TestScaleMealsForDayRejectsZeroHeadcount(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, _ := seedScalableDay(t, store, hh.ID)

	if _, err := store.ScaleMealsForDay(ctx, planID, "2026-01-05", 0); err == nil {
		t.Fatal("want an error for headcount 0, got nil")
	}
}
