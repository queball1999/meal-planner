package plan

import (
	"context"
	"strings"
	"testing"
)

// Every generated meal is filed in the recipe catalog, so a plan is not the
// only place a week's cooking survives.
func TestGenerateSavesRecipesToCatalog(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	if _, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "Plan"}, hhID, nil, nil, nil); err != nil {
		t.Fatalf("generate: %v", err)
	}

	recipes, err := store.ListCatalogRecipes(ctx, hhID)
	if err != nil {
		t.Fatalf("list recipes: %v", err)
	}
	if len(recipes) != 21 {
		t.Fatalf("saved %d recipes, want 21 (one per generated meal)", len(recipes))
	}

	var sample int64
	for _, r := range recipes {
		if r.SourceKind != "ai" {
			t.Errorf("recipe %q source = %q, want ai", r.Title, r.SourceKind)
		}
		if !strings.HasPrefix(r.Title, "Plan ") {
			t.Errorf("unexpected recipe title %q", r.Title)
		}
		sample = r.ID
	}

	// The recipe has to be cookable, not just a title: its ingredients and
	// steps come across too.
	ings, err := store.ListCatalogRecipeIngredients(ctx, sample)
	if err != nil {
		t.Fatalf("list recipe ingredients: %v", err)
	}
	if len(ings) != 1 || ings[0].Name != "canned black beans" {
		t.Errorf("recipe ingredients = %+v, want the one generated line", ings)
	}
	if ings[0].Quantity != "1" {
		t.Errorf("quantity = %q, want %q - a float formatted as 1.0000 reads badly", ings[0].Quantity, "1")
	}
	steps, err := store.ListCatalogRecipeSteps(ctx, sample)
	if err != nil {
		t.Fatalf("list recipe steps: %v", err)
	}
	if len(steps) != 1 || steps[0].Text != "Cook it." {
		t.Errorf("recipe steps = %+v, want the one generated step", steps)
	}
}

// The same meals come back week after week. Twenty copies of one title would
// make the recipes page useless, so an existing title is left alone.
func TestGenerateDoesNotDuplicateRecipeTitles(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	if _, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "Plan"}, hhID, nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	first, _ := store.ListCatalogRecipes(ctx, hhID)

	// Regenerating produces the same 21 titles.
	if _, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "Plan"}, hhID, nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	second, _ := store.ListCatalogRecipes(ctx, hhID)

	if len(second) != len(first) {
		t.Errorf("recipe count grew from %d to %d on a regenerate", len(first), len(second))
	}
}

// Linking used to happen only inside the pricer, and buildPricer returns nil
// when no store chain is configured - so a household with no stores never
// linked a single ingredient and every shopping line stayed unmatched. This
// generation runs with a nil pricer, which is exactly that case.
func TestGenerateLinksIngredientsWithoutAPricer(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	planID, err := generateTestWeek(ctx, store, &fakeGenerator{titlePrefix: "Plan"}, hhID, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	ings, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		t.Fatalf("list ingredients: %v", err)
	}
	if len(ings) == 0 {
		t.Fatal("no ingredients persisted")
	}
	for _, ing := range ings {
		if ing.ItemID == nil {
			t.Fatalf("ingredient %q has no catalog item - linking did not run", ing.Name)
		}
		if ing.NormalizedTerm == "" {
			t.Fatalf("ingredient %q has no normalized term", ing.Name)
		}
	}

	// All 21 meals used the same ingredient name, so it must resolve to one
	// item rather than 21 near-duplicates.
	seen := map[int64]bool{}
	for _, ing := range ings {
		seen[*ing.ItemID] = true
	}
	if len(seen) != 1 {
		t.Errorf("one ingredient name produced %d catalog items, want 1", len(seen))
	}
}
