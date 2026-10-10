package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"goeat/db"
)

// The picker's "add a new recipe": a link the household already imported comes
// back as that recipe instead of a second copy, a description needs an AI
// provider (the fixture has none), and a viewer cannot use it at all.
func TestRecipeNew(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()

	const link = "https://example.com/recipes/sloppy-joes"
	rc, err := f.store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: f.a.hh.ID, Title: "Sloppy Joes", SourceKind: "url", SourceURL: link, Servings: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	w := f.do("alice", "POST", "/plan/recipe-new", url.Values{"q": {link}})
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"recipe_id":`+strconv.FormatInt(rc.ID, 10)) || !strings.Contains(body, `"title":"Sloppy Joes"`) {
		t.Errorf("known link: %d %s, want the saved recipe", w.Code, body)
	}

	w = f.do("alice", "POST", "/plan/recipe-new", url.Values{"q": {"sloppy joes"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "No AI provider") {
		t.Errorf("description without a model: %d %s", w.Code, w.Body.String())
	}

	if w = f.do("alice", "POST", "/plan/recipe-new", url.Values{"q": {" "}}); w.Code != http.StatusBadRequest {
		t.Errorf("empty request: %d, want 400", w.Code)
	}
	if w = f.do("vera", "POST", "/plan/recipe-new", url.Values{"q": {link}}); w.Code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", w.Code)
	}
}
