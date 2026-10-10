package plan

import (
	"context"
	"strings"
	"testing"
)

// A few words become a saved recipe, fenced JSON and a missing unit included;
// asking again finds the one already saved rather than writing a second.
func TestWriteRecipeFromRequestSavesARecipe(t *testing.T) {
	store, hh, _ := newMatStore(t)
	ctx := context.Background()

	reply := "```json\n" + `{"title": "Sloppy Joes", "effort": "quick", "servings": 4, "prep_minutes": 5, "cook_minutes": 20,
 "ingredients": [{"name": "ground beef", "quantity": 1, "unit": "lb"}, {"name": "hamburger buns", "quantity": 4}],
 "steps": ["Brown the beef.", "Simmer with sauce.", "Spoon onto buns."]}` + "\n```"
	gen := &scriptedGenerator{replies: []string{reply}}

	rec, err := WriteRecipeFromRequest(ctx, store, gen, hh.ID, "sloppy joe", 2)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "Sloppy Joes" || rec.Reused || rec.ID == 0 {
		t.Fatalf("recipe = %+v, want a new Sloppy Joes", rec)
	}
	if !strings.Contains(gen.prompts[0], `"sloppy joe"`) {
		t.Errorf("prompt does not carry the request: %s", gen.prompts[0])
	}
	saved, _ := store.GetCatalogRecipe(ctx, rec.ID)
	if saved == nil || saved.Servings != 4 || saved.SourceKind != "ai" {
		t.Fatalf("saved recipe = %+v, want 4 servings from the AI", saved)
	}
	ings, _ := store.ListCatalogRecipeIngredients(ctx, rec.ID)
	if len(ings) != 2 || ings[1].Name != "hamburger buns" || ings[1].Unit != "each" {
		t.Errorf("ingredients = %+v, want buns counted in each", ings)
	}
	steps, _ := store.ListCatalogRecipeSteps(ctx, rec.ID)
	if len(steps) != 3 {
		t.Errorf("got %d steps, want 3", len(steps))
	}

	again, err := WriteRecipeFromRequest(ctx, store, gen, hh.ID, "sloppy joes please", 2)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != rec.ID || !again.Reused {
		t.Errorf("second ask = %+v, want the saved recipe reused", again)
	}
}

// Prose instead of a recipe is an error, and nothing is saved.
func TestWriteRecipeFromRequestRejectsNonsense(t *testing.T) {
	store, hh, _ := newMatStore(t)
	ctx := context.Background()

	gen := &scriptedGenerator{replies: []string{"I can't help with that."}}
	if _, err := WriteRecipeFromRequest(ctx, store, gen, hh.ID, "sloppy joe", 2); err == nil {
		t.Fatal("expected an error for a reply with no recipe in it")
	}
	if all, _ := store.ListCatalogRecipes(ctx, hh.ID); len(all) != 0 {
		t.Errorf("%d recipes saved, want none", len(all))
	}
}
