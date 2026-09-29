package web

import (
	"fmt"
	"io"
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
	// populated on GET after a successful import or notify redirect
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
		s.setNotify(w, NotifyDanger, "Please enter a URL.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		s.setNotify(w, NotifyDanger, "Invalid URL.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}

	id, err := recipes.Import(r.Context(), s.store, hh.ID, rawURL, s.imageDir)
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Import failed: %v", err))
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
		s.setNotify(w, NotifyDanger, "Recipe title is required.")
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
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Could not save recipe: %v", err))
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

// ── Recipe catalog list & detail (§5.7) ──────────────────────────────────────

type recipesPageData struct {
	Recipes   []*db.CatalogRecipe
	FilterQ   string
	FilterTag string
	FilterSrc string
	Filtered  bool
	Page      Pagination
}

// excludeLeftoverRecipes drops any catalog recipe whose title marks it as an
// intentional leftovers meal (plan/prompt.go's leftover-tolerance rules have
// the generator note this in the title) from a user-facing recipe list.
// These are meal-plan artifacts of a specific batch-cook, not something
// anyone picks to cook standalone - the recipe catalog, the slot-filling
// picker, and the account page's recipe count should all skip them.
func excludeLeftoverRecipes(recipes []*db.CatalogRecipe) []*db.CatalogRecipe {
	out := recipes[:0]
	for _, rc := range recipes {
		if strings.Contains(strings.ToLower(rc.Title), "leftover") {
			continue
		}
		out = append(out, rc)
	}
	return out
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
	list = excludeLeftoverRecipes(list)

	list, page := paginate(r, list)

	s.render(w, r, "recipes", recipesPageData{
		Recipes:   list,
		Page:      page,
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
	if !s.owns(w, r, db.ResRecipe, id) {
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
	if !s.owns(w, r, db.ResRecipe, id) {
		return
	}
	_ = s.store.DeleteCatalogRecipe(r.Context(), id)
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

// handleRecipeEdit rewrites a recipe's fields plus its ingredient and step
// lists from the edit form on the detail page.
func (s *Server) handleRecipeEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if !s.owns(w, r, db.ResRecipe, id) {
		return
	}
	ctx := r.Context()

	cr, err := s.store.GetCatalogRecipe(ctx, id)
	if err != nil || cr == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Could not read form data.")
		http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = cr.Title
	}
	servings, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("servings")))
	prep, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("prep_minutes")))
	cook, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("cook_minutes")))

	var tags []string
	for _, t := range strings.Split(r.FormValue("tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}

	if err := s.store.UpdateCatalogRecipe(ctx, db.UpdateCatalogRecipeParams{
		ID:          cr.ID,
		Title:       title,
		Servings:    servings,
		PrepMinutes: prep,
		CookMinutes: cook,
		Tags:        tags,
	}); err != nil {
		s.setNotify(w, NotifyDanger, "Could not save recipe.")
		http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
		return
	}

	ings := splitLines(r.FormValue("ingredients"))
	steps := splitLines(r.FormValue("steps"))
	if err := recipes.ReplaceContent(ctx, s.store, cr.ID, ings, steps); err != nil {
		s.setNotify(w, NotifyWarning, fmt.Sprintf("Recipe saved, but ingredients/steps failed: %v", err))
	} else {
		s.setNotify(w, NotifySuccess, "Recipe updated.")
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

// handleRecipeReimport re-fetches the source page and replaces the recipe's
// ingredients and steps - repairs recipes imported before the parser fix.
func (s *Server) handleRecipeReimport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if !s.owns(w, r, db.ResRecipe, id) {
		return
	}
	if err := recipes.Refresh(r.Context(), s.store, id, s.imageDir); err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Re-import failed: %v", err))
	} else {
		s.setNotify(w, NotifySuccess, "Recipe re-imported from source.")
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

// handleRecipeImageReplace accepts either an uploaded file or an image URL and
// swaps the recipe's image, deleting the previous file.
func (s *Server) handleRecipeImageReplace(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if !s.owns(w, r, db.ResRecipe, id) {
		return
	}
	ctx := r.Context()

	cr, err := s.store.GetCatalogRecipe(ctx, id)
	if err != nil || cr == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil && r.MultipartForm == nil {
		_ = r.ParseForm()
	}

	// A caller off the catalog page returns there; a meal page attaching a
	// photo to its matching catalog recipe wants to land back on itself.
	dest := fmt.Sprintf("/recipes/%d", id)
	if rt := r.FormValue("redirect_to"); rt != "" {
		dest = rt
	}

	// "remove" wins over any supplied source.
	if r.FormValue("remove") == "1" {
		if err := s.store.ClearCatalogRecipeImage(ctx, id); err != nil {
			s.setNotify(w, NotifyDanger, "Could not remove image.")
		} else {
			recipes.RemoveImage(s.imageDir, cr.ImagePath)
			s.setNotify(w, NotifySuccess, "Image removed.")
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}

	var newName string
	if f, fh, ferr := r.FormFile("image_file"); ferr == nil {
		defer f.Close()
		data, rerr := io.ReadAll(io.LimitReader(f, 8<<20))
		if rerr != nil {
			s.setNotify(w, NotifyDanger, "Could not read the uploaded file.")
			http.Redirect(w, r, dest, http.StatusSeeOther)
			return
		}
		newName, err = recipes.SaveImageBytes(s.imageDir, fh.Filename, data)
	} else if raw := strings.TrimSpace(r.FormValue("image_url")); raw != "" {
		newName, err = recipes.DownloadImage(ctx, raw, s.imageDir)
	} else {
		s.setNotify(w, NotifyDanger, "Choose a file or paste an image URL.")
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Image failed: %v", err))
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}

	if err := s.store.UpdateCatalogRecipe(ctx, db.UpdateCatalogRecipeParams{
		ID:          cr.ID,
		Title:       cr.Title,
		Servings:    cr.Servings,
		PrepMinutes: cr.PrepMinutes,
		CookMinutes: cr.CookMinutes,
		Tags:        cr.Tags,
		ImagePath:   newName,
	}); err != nil {
		s.setNotify(w, NotifyDanger, "Could not save the new image.")
		recipes.RemoveImage(s.imageDir, newName)
	} else {
		if cr.ImagePath != "" && cr.ImagePath != newName {
			recipes.RemoveImage(s.imageDir, cr.ImagePath)
		}
		s.setNotify(w, NotifySuccess, "Image updated.")
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// splitLines turns a textarea value into trimmed, non-empty lines.
func splitLines(v string) []string {
	var out []string
	for _, line := range strings.Split(v, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// handleRecipeImageServe serves images from the runtime imageDir.
func (s *Server) handleRecipeImageServe(w http.ResponseWriter, r *http.Request) {
	s.serveHouseholdImage(w, r, s.imageDir, db.ImageRecipe)
}
