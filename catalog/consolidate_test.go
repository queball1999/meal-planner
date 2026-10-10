package catalog_test

import (
	"context"
	"testing"

	"goeat/catalog"
	"goeat/db"
	"goeat/llm"
)

// cannedGen answers every call with one fixed reply.
type cannedGen struct{ reply string }

func (g cannedGen) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{Content: g.reply}, nil
}
func (cannedGen) ProviderName() string { return "test" }
func (cannedGen) ModelName() string    { return "test" }

// planWithIngredients is a one-meal plan whose ingredients are names, in order
// - which is the order the model is shown them in, so their ids are 0..n-1.
func planWithIngredients(t *testing.T, store db.Store, hhID int64, names ...string) (*db.Plan, *db.Meal) {
	t.Helper()
	ctx := context.Background()
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: hhID, WeekStart: "2099-01-04", WeekEnd: "2099-01-10", BudgetCents: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: p.ID, Day: "2099-01-04", Slot: "dinner", Title: "Stir Fry", Effort: "quick", Servings: 2, CookedPortions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID: m.ID, Name: name, Quantity: 1, Unit: "tsp",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return p, m
}

// Three names for black pepper on one plan become one item, the meals and the
// saved recipe are renamed to it, and bell pepper - which the model did not
// group - is untouched.
func TestConsolidatePlanItemsMergesAndRenames(t *testing.T) {
	store, hhID := newStore(t)
	ctx := context.Background()

	p, m := planWithIngredients(t, store, hhID, "Black pepper", "pepper", "salt and pepper", "bell pepper")
	rc, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{HouseholdID: hhID, Title: "Stir Fry", Servings: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"salt and pepper", "bell pepper"} {
		if err := store.AddCatalogRecipeIngredient(ctx, rc.ID, name, "1", "tsp", i); err != nil {
			t.Fatal(err)
		}
	}

	// The out-of-range id and the repeat of 2 are dropped, not acted on.
	gen := cannedGen{reply: "```json\n[{\"keep\": 0, \"merge\": [1, 2, 9]}, {\"keep\": 3, \"merge\": [2]}]\n```"}
	got, err := catalog.ConsolidatePlanItems(ctx, store, gen, hhID, p.ID, "2099-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kept.Name != "Black pepper" || len(got[0].Merged) != 2 || len(got[0].Skipped) != 0 {
		t.Fatalf("consolidations = %+v, want pepper and salt and pepper merged into Black pepper", got)
	}

	ings, _ := store.ListIngredientsByMeal(ctx, m.ID)
	names := map[string]int{}
	items := map[int64]bool{}
	for _, ing := range ings {
		names[ing.Name]++
		if ing.ItemID != nil {
			items[*ing.ItemID] = true
		}
	}
	if names["Black pepper"] != 3 || names["bell pepper"] != 1 || len(items) != 2 {
		t.Errorf("meal ingredients = %v over %d items, want 3 Black pepper and 1 bell pepper over 2 items", names, len(items))
	}

	recipeIngs, _ := store.ListCatalogRecipeIngredients(ctx, rc.ID)
	if len(recipeIngs) != 2 || recipeIngs[0].Name != "Black pepper" || recipeIngs[1].Name != "bell pepper" {
		t.Errorf("recipe ingredients = %+v, want Black pepper and bell pepper", recipeIngs)
	}

	// The old name now resolves to the kept item rather than a new placeholder.
	it, err := catalog.EnsureItem(ctx, store, hhID, "salt and pepper")
	if err != nil || it == nil || it.ID != got[0].Kept.ID {
		t.Errorf("EnsureItem(salt and pepper) = %+v, %v; want the kept Black pepper item", it, err)
	}
}

// A real catalog item is never merged away on the model's word: it is the one
// kept even when the model picked a placeholder, and a second real item in
// the same set is left standing.
func TestConsolidatePlanItemsKeepsRealItems(t *testing.T) {
	store, hhID := newStore(t)
	ctx := context.Background()

	for _, it := range []struct{ name, term, source string }{
		{"Black pepper", "black pepper", "builtin"},
		{"White pepper", "white pepper", "manual"},
	} {
		if _, err := store.CreateItem(ctx, db.CreateItemParams{
			HouseholdID: hhID, Name: it.name, NormalizedTerm: it.term, StockUnit: "g", DefaultPurchaseQty: 1, Source: it.source,
		}); err != nil {
			t.Fatal(err)
		}
	}
	p, m := planWithIngredients(t, store, hhID, "pepper", "black pepper", "white pepper")

	// The model keeps the placeholder "pepper" (id 0).
	got, err := catalog.ConsolidatePlanItems(ctx, store, cannedGen{reply: `[{"keep": 0, "merge": [1, 2]}]`}, hhID, p.ID, "2099-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kept.Name != "Black pepper" ||
		len(got[0].Merged) != 1 || got[0].Merged[0] != "pepper" ||
		len(got[0].Skipped) != 1 || got[0].Skipped[0] != "White pepper" {
		t.Fatalf("consolidations = %+v, want pepper merged into Black pepper and White pepper skipped", got)
	}
	if it, _ := store.GetItemByTerm(ctx, hhID, "white pepper"); it == nil {
		t.Error("the household's own White pepper item was deleted")
	}
	if it, _ := store.GetItemByTerm(ctx, hhID, "pepper"); it != nil {
		t.Error("the pepper placeholder is still in the catalog")
	}
	ings, _ := store.ListIngredientsByMeal(ctx, m.ID)
	if len(ings) != 3 || ings[0].Name != "Black pepper" || ings[1].Name != "Black pepper" || ings[2].Name != "white pepper" {
		t.Errorf("meal ingredients = %+v, want Black pepper twice and white pepper", ings)
	}
}
