package web

import (
	"testing"

	"goeat/db"
)

// TestExcludeLeftoverRecipes covers the /recipes, recipe-picker, and account
// recipe-count filter: a title marking an intentional-leftovers meal (see
// plan/prompt.go) should never show up in a user-facing recipe list,
// case-insensitively, while an ordinary recipe - including one that just
// happens to mention leftovers in passing - is judged by the same simple
// substring rule the generator's own title convention relies on.
func TestExcludeLeftoverRecipes(t *testing.T) {
	in := []*db.CatalogRecipe{
		{ID: 1, Title: "Weeknight Chili"},
		{ID: 2, Title: "Taco Bowls (Leftovers)"},
		{ID: 3, Title: "Chicken Stir-Fry"},
		{ID: 4, Title: "Monday Leftover Soup"},
		{ID: 5, Title: "LEFTOVERS: Pasta Bake"},
	}

	out := excludeLeftoverRecipes(in)

	if len(out) != 2 {
		t.Fatalf("got %d recipes, want 2 (leftovers excluded): %+v", len(out), out)
	}
	for _, rc := range out {
		if rc.ID != 1 && rc.ID != 3 {
			t.Errorf("unexpected recipe survived the filter: %+v", rc)
		}
	}
}
