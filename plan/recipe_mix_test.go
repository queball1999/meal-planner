package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goeat/db"
)

func seedMixRecipe(t *testing.T, store db.Store, hhID int64, title string, servings int, ings [][3]string) *db.CatalogRecipe {
	t.Helper()
	ctx := context.Background()
	rc, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: hhID, Title: title, SourceKind: "manual", Servings: servings,
		PrepMinutes: 10, CookMinutes: 30, Tags: []string{"dinner"},
	})
	if err != nil {
		t.Fatalf("seed recipe %q: %v", title, err)
	}
	for i, ing := range ings {
		if err := store.AddCatalogRecipeIngredient(ctx, rc.ID, ing[0], ing[1], ing[2], i); err != nil {
			t.Fatalf("seed ingredient: %v", err)
		}
	}
	if err := store.AddCatalogRecipeStep(ctx, rc.ID, 0, "Simmer for 30 minutes."); err != nil {
		t.Fatalf("seed step: %v", err)
	}
	return rc
}

// The slider's two ends are absolute, and the middle follows the roll.
func TestRollRecipeMix(t *testing.T) {
	recipes := make([]*db.CatalogRecipe, 30)
	for i := range recipes {
		recipes[i] = &db.CatalogRecipe{ID: int64(i + 1), Title: "R"}
	}
	half := func() float64 { return 0.5 }

	if mix := rollRecipeMix(recipes, 21, 0, half); mix.Saved != 21 || mix.New != 0 {
		t.Errorf("novelty 0: got %d saved / %d new, want 21 / 0", mix.Saved, mix.New)
	}
	if mix := rollRecipeMix(recipes, 21, 1, half); mix.Saved != 0 || mix.New != 21 || len(mix.Recipes) != 0 {
		t.Errorf("novelty 1: got %d saved / %d new, want 0 / 21 and no list", mix.Saved, mix.New)
	}

	// Rolls of 0.0, 0.1 ... 0.9 against a 30% chance: three land under it.
	n := 0
	steps := func() float64 { n++; return float64(n-1) / 10 }
	if mix := rollRecipeMix(recipes, 10, 0.3, steps); mix.New != 3 || mix.Saved != 7 {
		t.Errorf("novelty 0.3: got %d saved / %d new, want 7 / 3", mix.Saved, mix.New)
	}

	// A recipe is used once a week, so two of them cannot fill 21 slots.
	if mix := rollRecipeMix(recipes[:2], 21, 0, half); mix.Saved != 2 || mix.New != 19 {
		t.Errorf("2 recipes: got %d saved / %d new, want 2 / 19", mix.Saved, mix.New)
	}

	// Every meal already asked for, or nothing saved: nothing to decide.
	if rollRecipeMix(recipes, 0, 0.5, half) != nil || rollRecipeMix(nil, 21, 0.5, half) != nil {
		t.Error("want no mix with no free slots or no saved recipes")
	}
}

// A recipe the household cannot eat is never offered - Validate would throw
// the whole plan out over it - and neither is one already picked.
func TestUsableRecipesSkipsForbiddenAndPicked(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	chili := seedMixRecipe(t, store, hhID, "Weeknight Chili", 4, [][3]string{{"black beans", "2", "can"}})
	satay := seedMixRecipe(t, store, hhID, "Chicken Satay", 4, [][3]string{{"peanut butter", "0.5", "cup"}})
	picked := seedMixRecipe(t, store, hhID, "Lasagna", 4, [][3]string{{"pasta sheets", "12", "each"}})
	seedMixRecipe(t, store, hhID, "Leftover Chili", 2, [][3]string{{"chili", "2", "cup"}})

	got, err := usableRecipes(ctx, store, hhID, &PreferenceProfile{Allergies: []string{"peanut"}}, map[int64]bool{picked.ID: true})
	if err != nil {
		t.Fatalf("usableRecipes: %v", err)
	}
	if len(got) != 1 || got[0].ID != chili.ID {
		t.Errorf("got %d recipes, want only the chili (not %q, the picked one, or leftovers)", len(got), satay.Title)
	}
}

// The prompt lists the saved recipes with their ids and the split to hit.
func TestPromptIncludesRecipeMix(t *testing.T) {
	hh := &db.Household{HouseholdSize: 2, WeeklyBudgetCents: 15000}
	start := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	mix := &RecipeMix{Saved: 5, New: 15, Recipes: []*db.CatalogRecipe{
		{ID: 12, Title: "Weeknight Chili", Tags: []string{"dinner"}, CookMinutes: 40},
	}}
	_, user := BuildPrompt(hh, &PreferenceProfile{}, nil, start, start.AddDate(0, 0, 6),
		[]string{"something with salmon on Friday"}, nil, nil, mix, nil)

	for _, want := range []string{
		"of the 20 meals not already requested above, 5 must come from the household's saved recipes",
		"other 15 must be new dishes",
		"- [12] Weeknight Chili (dinner; 40 min)",
		"recipe_id",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt missing %q:\n%s", want, user)
		}
	}
}

// A meal the model wrote as a bare recipe_id comes out as the saved recipe
// itself - its title, steps, and ingredients scaled to the household - and is
// linked to it rather than filed as a new recipe.
func TestGenerate_ExpandsSavedRecipeReference(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	chili := seedMixRecipe(t, store, hhID, "Weeknight Chili", 4, [][3]string{
		{"black beans", "2", "can"},
		{"eggs", "4", ""},
	})

	var gp GeneratedPlan
	if err := json.Unmarshal([]byte(onePlan("New")), &gp); err != nil {
		t.Fatalf("build plan: %v", err)
	}
	gp.Meals[2] = GeneratedMeal{Day: "sunday", Slot: "dinner", RecipeID: chili.ID, Title: "chili"}
	raw, _ := json.Marshal(gp)

	planID, err := generateTestWeek(ctx, store, &scriptedGenerator{replies: []string{string(raw)}}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil {
		t.Fatalf("list meals: %v", err)
	}
	var meal *db.Meal
	for _, m := range meals {
		if m.Title == "Weeknight Chili" {
			meal = m
		}
	}
	if meal == nil {
		t.Fatal("no meal carries the saved recipe's title")
	}
	if linked, err := store.ListMealsByCatalogRecipe(ctx, chili.ID, "2000-01-01"); err != nil || len(linked) != 1 || linked[0].ID != meal.ID {
		t.Errorf("meal is not linked to the saved recipe: %d linked (err %v)", len(linked), err)
	}
	if meal.Servings != 2 {
		t.Errorf("servings = %d, want the household's 2", meal.Servings)
	}

	ings, err := store.ListIngredientsByMeal(ctx, meal.ID)
	if err != nil || len(ings) != 2 {
		t.Fatalf("got %d ingredients (err %v), want the recipe's 2", len(ings), err)
	}
	for _, ing := range ings {
		switch ing.Name {
		case "black beans":
			if ing.Quantity != 1 || ing.Unit != "can" {
				t.Errorf("black beans = %v %s, want the 4-serving recipe's 2 cans halved", ing.Quantity, ing.Unit)
			}
		case "eggs":
			if ing.Quantity != 2 || ing.Unit != "each" {
				t.Errorf("eggs = %v %q, want 2 each", ing.Quantity, ing.Unit)
			}
		}
	}

	all, err := store.ListCatalogRecipes(ctx, hhID)
	if err != nil {
		t.Fatalf("list recipes: %v", err)
	}
	n := 0
	for _, rc := range all {
		if rc.Title == "Weeknight Chili" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d catalog entries titled Weeknight Chili, want the original only", n)
	}
}

// A reference to a recipe that is not there cannot be cooked.
func TestExpandSavedRecipesRejectsUnknownID(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	meals := []GeneratedMeal{{Day: "sunday", Slot: "dinner", RecipeID: 999}}
	if err := expandSavedRecipes(ctx, store, hhID, meals, 2); err == nil {
		t.Error("want an error for a recipe id that does not exist")
	}
}

// A "just the remaining days" regenerate is told what the earlier days of the
// week had, so it does not serve one of them again - and not what the days it
// is replacing had, which are free to come back.
func TestGenerate_MidWeekPromptListsEarlierMeals(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	if _, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "First"}, hhID, nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}

	var meals []GeneratedMeal
	for _, day := range []string{"wednesday", "thursday", "friday", "saturday"} {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			meals = append(meals, GeneratedMeal{
				Day: day, Slot: slot, Title: "Second " + day + " " + slot,
				Effort: "quick", Servings: 2, CookedPortions: 2,
				Ingredients: []GeneratedIngredient{{Name: "rice", Quantity: 1, Unit: "cup"}},
				Steps:       []string{"Cook it."},
			})
		}
	}
	raw, _ := json.Marshal(GeneratedPlan{Meals: meals})
	gen := &scriptedGenerator{replies: []string{string(raw)}}

	wednesday := testWeek.AddDate(0, 0, 3)
	if _, err := GenerateForWeek(ctx, store, gen, hhID, testWeek, wednesday, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("mid-week generate: %v", err)
	}

	prompt := gen.prompts[0]
	for _, want := range []string{"Meals already had earlier this week", "monday dinner: First monday dinner"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("mid-week prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "thursday dinner: First thursday dinner") {
		t.Error("mid-week prompt lists a meal from a day that is being replanned")
	}
	// The earlier meals were saved as recipes too; none may be offered back.
	if strings.Contains(prompt, "] First monday dinner") {
		t.Error("a recipe already cooked earlier this week was offered as a saved recipe")
	}
}
