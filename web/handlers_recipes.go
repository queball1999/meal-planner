package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"goeat/db"
	"goeat/middleware"
	"goeat/recipes"
)

// ── Recipe import (§5.7) ──────────────────────────────────────────────────────

type recipeImportPageData struct {
	// populated on GET after a successful import or flash redirect
}

func (s *Server) handleRecipeImportPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "recipe_import", recipeImportPageData{})
}

func (s *Server) handleRecipeImport(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		s.setFlash(w, "Please enter a URL.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		s.setFlash(w, "Invalid URL.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}

	id, err := recipes.Import(r.Context(), s.store, hh.ID, rawURL, s.imageDir)
	if err != nil {
		s.setFlash(w, fmt.Sprintf("Import failed: %v", err))
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

func (s *Server) handleRecipeImportManual(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		s.setFlash(w, "Recipe title is required.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}

	servings, _ := strconv.Atoi(r.FormValue("servings"))
	if servings <= 0 {
		servings = 4
	}
	prepMin, _ := strconv.Atoi(r.FormValue("prep_minutes"))
	cookMin, _ := strconv.Atoi(r.FormValue("cook_minutes"))

	rawIngs := strings.TrimSpace(r.FormValue("ingredients"))
	var ings []string
	for _, line := range strings.Split(rawIngs, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ings = append(ings, line)
		}
	}

	rawSteps := strings.TrimSpace(r.FormValue("steps"))
	var steps []string
	for _, line := range strings.Split(rawSteps, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			steps = append(steps, line)
		}
	}

	tagsRaw := strings.TrimSpace(r.FormValue("tags"))
	var tags []string
	for _, t := range strings.Split(tagsRaw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}

	id, err := recipes.SaveManual(r.Context(), s.store, hh.ID, db.CreateCatalogRecipeParams{
		HouseholdID: hh.ID,
		Title:       title,
		Servings:    servings,
		PrepMinutes: prepMin,
		CookMinutes: cookMin,
		Tags:        tags,
	}, ings, steps)
	if err != nil {
		s.setFlash(w, fmt.Sprintf("Could not save recipe: %v", err))
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

// ── Recipe catalog list & detail (§5.7) ──────────────────────────────────────

type recipesPageData struct {
	Recipes    []*db.CatalogRecipe
	FilterQ    string
	FilterTag  string
	FilterSrc  string
	Filtered   bool
}

func (s *Server) handleRecipesPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	src := strings.TrimSpace(r.URL.Query().Get("source"))

	filtered := q != "" || tag != "" || src != ""
	var list []*db.CatalogRecipe
	if filtered {
		list, _ = s.store.FilterCatalogRecipes(r.Context(), hh.ID, db.CatalogRecipeFilter{Q: q, Tag: tag, Source: src})
	} else {
		list, _ = s.store.ListCatalogRecipes(r.Context(), hh.ID)
	}

	s.render(w, r, "recipes", recipesPageData{
		Recipes:   list,
		FilterQ:   q,
		FilterTag: tag,
		FilterSrc: src,
		Filtered:  filtered,
	})
}

type recipeDetailPageData struct {
	Recipe      *db.CatalogRecipe
	Ingredients []*db.CatalogRecipeIngredient
	Steps       []*db.CatalogRecipeStep
}

func (s *Server) handleRecipeDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	cr, err := s.store.GetCatalogRecipe(ctx, id)
	if err != nil || cr == nil {
		http.NotFound(w, r)
		return
	}
	ings, _ := s.store.ListCatalogRecipeIngredients(ctx, id)
	steps, _ := s.store.ListCatalogRecipeSteps(ctx, id)

	s.render(w, r, "recipe", recipeDetailPageData{Recipe: cr, Ingredients: ings, Steps: steps})
}

func (s *Server) handleRecipeDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	_ = s.store.DeleteCatalogRecipe(r.Context(), id)
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

// handleRecipeImageServe serves images from the runtime imageDir.
func (s *Server) handleRecipeImageServe(w http.ResponseWriter, r *http.Request) {
	if s.imageDir == "" {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, s.imageDir+"/"+name)
}
