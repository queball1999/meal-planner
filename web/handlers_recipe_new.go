package web

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"goeat/middleware"
	"goeat/plan"
	"goeat/recipes"
	"goeat/video"
)

// handleRecipeNew is the meal picker's "add a new recipe": the swap and fill
// dialogs list saved recipes, and this is for the dish that is not one of them
// yet. What was typed is either a link, imported exactly as the Recipes page
// imports it, or a few words ("sloppy joes"), which the model writes a recipe
// for (plan.WriteRecipeFromRequest).
//
// It only saves the recipe and answers with its id. The page then puts it in
// the slot by the same form a saved recipe is picked with, so the swap's
// leftover questions and the scaling to the day are not a second code path.
//
//	POST /plan/recipe-new  {q}
func (s *Server) handleRecipeNew(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Describe a dish or paste a recipe link."})
		return
	}
	// An import fetches a page and a described dish waits on the model; either
	// can outlast the server's ordinary write timeout.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
	ctx := r.Context()

	found := func(id int64, note string) {
		title := ""
		if cr, _ := s.store.GetCatalogRecipe(ctx, id); cr != nil {
			title = cr.Title
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "recipe_id": id, "title": title, "note": note})
	}

	if u, err := url.ParseRequestURI(q); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		if cr, err := recipes.FindBySource(ctx, s.store, hh.ID, q); err == nil && cr != nil {
			found(cr.ID, "already in your recipes")
			return
		}
		if video.IsVideoURL(q) {
			// A video import runs in the background with its own progress
			// page; there is no recipe to hand back from here.
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false,
				"error": "Video links take a while to import. Import it on the Recipes page, then pick it here."})
			return
		}
		id, err := recipes.Import(ctx, s.store, hh.ID, q, s.imageDir)
		var dup *recipes.DuplicateError
		switch {
		case errors.As(err, &dup):
			found(dup.ID, "already in your recipes")
		case err != nil:
			log.Printf("recipe new: import %q: %v", q, err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "Import failed: " + err.Error()})
		default:
			found(id, "imported")
		}
		return
	}

	gen := s.llmGen()
	if gen == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false,
			"error": "No AI provider is configured to write a recipe. Paste a recipe link instead."})
		return
	}
	rec, err := plan.WriteRecipeFromRequest(ctx, s.store, gen, hh.ID, q, hh.HouseholdSize)
	if err != nil {
		log.Printf("recipe new: write %q: %v", q, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "Couldn't write that recipe: " + err.Error()})
		return
	}
	note := "written by the AI"
	if rec.Reused {
		note = "already in your recipes"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "recipe_id": rec.ID, "title": rec.Title, "note": note})
}
