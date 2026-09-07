package plan

import (
	"context"
	"math"
	"testing"

	"goeat/db"
)

func newMatStore(t *testing.T) (db.Store, *db.Household, int64) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T", HouseholdSize: 2})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	plan, err := store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: hh.ID, WeekStart: "2026-01-04", WeekEnd: "2026-01-10",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return store, hh, plan.ID
}

// seedRecipe writes a 4-serving chili with three ingredient lines, one of them
// unquantified.
func seedRecipe(t *testing.T, store db.Store, householdID int64) int64 {
	t.Helper()
	ctx := context.Background()
	r, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: householdID,
		Title:       "Weeknight Chili",
		SourceKind:  "manual",
		Servings:    4,
		PrepMinutes: 10,
		CookMinutes: 40,
	})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	lines := []struct{ name, qty, unit string }{
		{"ground beef", "2", "lb"},
		{"kidney beans", "1 1/2", "cup"},
		{"salt", "to taste", ""},
	}
	for i, ln := range lines {
		if err := store.AddCatalogRecipeIngredient(ctx, r.ID, ln.name, ln.qty, ln.unit, i); err != nil {
			t.Fatalf("add ingredient %s: %v", ln.name, err)
		}
	}
	for i, step := range []string{"Brown the beef.", "Simmer everything."} {
		if err := store.AddCatalogRecipeStep(ctx, r.ID, i, step); err != nil {
			t.Fatalf("add step: %v", err)
		}
	}
	return r.ID
}

func ingredientsByName(t *testing.T, store db.Store, mealID int64) map[string]*db.MealIngredient {
	t.Helper()
	list, err := store.ListIngredientsByMeal(context.Background(), mealID)
	if err != nil {
		t.Fatalf("list ingredients: %v", err)
	}
	out := make(map[string]*db.MealIngredient, len(list))
	for _, ing := range list {
		out[ing.Name] = ing
	}
	return out
}

func TestMaterializeScalesToDayPortions(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)

	// A 4-serving recipe onto a day feeding 3.0 portions: everything x0.75.
	res, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "dinner", Portions: 3.0,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if res.Servings != 3 {
		t.Errorf("servings = %d, want 3", res.Servings)
	}
	if res.Ingredients != 3 {
		t.Errorf("ingredients = %d, want 3", res.Ingredients)
	}
	if len(res.Unquantified) != 1 || res.Unquantified[0] != "salt" {
		t.Errorf("unquantified = %v, want [salt]", res.Unquantified)
	}

	ing := ingredientsByName(t, store, res.MealID)
	if got := ing["ground beef"].Quantity; math.Abs(got-1.5) > 1e-9 {
		t.Errorf("ground beef = %v, want 1.5", got)
	}
	if got := ing["kidney beans"].Quantity; math.Abs(got-1.125) > 1e-9 {
		t.Errorf("kidney beans = %v, want 1.125", got)
	}
	// An unquantified line is kept at 0 rather than dropped: losing an
	// ingredient silently would be worse than one that cannot be priced.
	if _, ok := ing["salt"]; !ok {
		t.Error("the unquantified line was dropped")
	}
}

// The as-created amounts become the baseline, so the first headcount change
// scales from them instead of finding zeros and skipping the meal.
func TestMaterializeSetsRescaleBaseline(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)

	res, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "dinner", Portions: 4,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	scaled, err := store.ScaleMealsForDay(ctx, planID, "2026-01-05", 2)
	if err != nil {
		t.Fatalf("rescale: %v", err)
	}
	if scaled.MealsScaled != 1 {
		t.Fatalf("scaled %d meals, want 1 - the baseline was never set", scaled.MealsScaled)
	}
	ing := ingredientsByName(t, store, res.MealID)
	if got := ing["ground beef"].Quantity; math.Abs(got-1.0) > 1e-9 {
		t.Errorf("ground beef after halving = %v, want 1.0", got)
	}
}

// Filling a slot means filling it - two meals stacked in one slot would render
// as whichever the calendar query happened to see first.
func TestMaterializeReplacesExistingSlot(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)
	recipeID := seedRecipe(t, store, hh.ID)

	if _, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-05", Slot: "dinner",
		Title: "Something else", Effort: "quick", Servings: 2, CookedPortions: 2,
	}); err != nil {
		t.Fatalf("seed meal: %v", err)
	}

	if _, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: recipeID,
		Date: "2026-01-05", Slot: "dinner", Portions: 2,
	}); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	meals, _ := store.ListMealsByPlan(ctx, planID)
	var inSlot int
	for _, m := range meals {
		if m.Day == "2026-01-05" && m.Slot == "dinner" {
			inSlot++
			if m.Title != "Weeknight Chili" {
				t.Errorf("slot holds %q, want Weeknight Chili", m.Title)
			}
		}
	}
	if inSlot != 1 {
		t.Errorf("%d meals in the slot, want 1", inSlot)
	}
}

// A recipe from another household must not be reachable by guessing its id.
func TestMaterializeRejectsForeignRecipe(t *testing.T) {
	ctx := context.Background()
	store, hh, planID := newMatStore(t)

	other, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "Other"})
	if err != nil {
		t.Fatalf("other household: %v", err)
	}
	foreign := seedRecipe(t, store, other.ID)

	if _, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID: planID, HouseholdID: hh.ID, CatalogRecipeID: foreign,
		Date: "2026-01-05", Slot: "dinner", Portions: 2,
	}); err == nil {
		t.Error("materialized another household's recipe")
	}
}

func TestScaleFor(t *testing.T) {
	cases := []struct {
		name         string
		baseServings int
		portions     float64
		wantServings int
		wantFactor   float64
	}{
		{"scales down", 4, 3, 3, 0.75},
		{"scales up", 2, 5, 5, 2.5},
		// No day context: take the recipe as written rather than guessing.
		{"no portions", 4, 0, 4, 1},
		// A recipe with no stated yield has no denominator, so quantities are
		// left alone - multiplying by a guessed base would silently inflate.
		{"no recipe yield", 0, 3, 3, 1},
		{"neither", 0, 0, 1, 1},
	}
	for _, c := range cases {
		s, f := scaleFor(c.baseServings, c.portions)
		if s != c.wantServings || math.Abs(f-c.wantFactor) > 1e-9 {
			t.Errorf("%s: scaleFor(%d, %v) = (%d, %v), want (%d, %v)",
				c.name, c.baseServings, c.portions, s, f, c.wantServings, c.wantFactor)
		}
	}
}

func TestEffortFor(t *testing.T) {
	cases := []struct {
		prep, cook int
		want       string
	}{
		{5, 5, "quick"},
		{10, 20, "standard"},
		{30, 60, "elaborate"},
		// Nothing recorded is "standard", the neutral middle - claiming a
		// recipe is quick because it has no times would be a lie.
		{0, 0, "standard"},
	}
	for _, c := range cases {
		got := effortFor(&db.CatalogRecipe{PrepMinutes: c.prep, CookMinutes: c.cook})
		if got != c.want {
			t.Errorf("effortFor(%d+%d) = %q, want %q", c.prep, c.cook, got, c.want)
		}
	}
}
