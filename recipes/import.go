// Package recipes handles recipe catalog management and web import (§5.7).
package recipes

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"goeat/db"
	"goeat/safefetch"
	"goeat/scrape"
	"goeat/video"
)

// Import fetches rawURL, parses a Recipe via scrape.ParseRecipe, optionally
// downloads the image, persists everything to the DB, and returns the new
// catalog recipe ID. Image download failures are non-fatal. A page the
// household already imported - by the pasted URL or the one it redirects to -
// returns a *DuplicateError instead of a second copy.
func Import(ctx context.Context, store db.Store, householdID int64, rawURL, imageDir string) (int64, error) {
	if err := checkDuplicate(ctx, store, householdID, rawURL); err != nil {
		return 0, err
	}

	// 1. Fetch the page.
	res, err := safefetch.Fetch(ctx, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("recipes: fetch %q: %w", rawURL, err)
	}
	if err := checkDuplicate(ctx, store, householdID, res.FinalURL); err != nil {
		return 0, err
	}

	// 2. Parse the recipe.
	recipe, err := scrape.ParseRecipe(string(res.Body), res.FinalURL)
	if err != nil {
		return 0, fmt.Errorf("recipes: parse: %w", err)
	}

	// 3. Download the image (non-fatal).
	imagePath := ""
	if recipe.ImageURL != "" && imageDir != "" {
		imagePath = downloadImage(ctx, recipe.ImageURL, imageDir)
	}

	// 4. Persist the recipe, its ingredients and steps.
	return save(ctx, store, householdID, recipe, res.FinalURL, imagePath)
}

// save writes an imported recipe - from a web page or a video - to the
// catalog and returns its ID. Ingredients that already carry a parsed name
// (a video import's model output) are stored as-is; raw lines from a web page
// go through splitIngredient.
func save(ctx context.Context, store db.Store, householdID int64, recipe *scrape.Recipe, sourceURL, imagePath string) (int64, error) {
	cr, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID:  householdID,
		Title:        recipe.Title,
		SourceKind:   "imported",
		SourceURL:    sourceURL,
		SourceSite:   recipe.SourceSite,
		SourceAuthor: recipe.Author,
		ImagePath:    imagePath,
		Servings:     recipe.Servings,
		PrepMinutes:  recipe.PrepMinutes,
		CookMinutes:  recipe.CookMinutes,
		Tags:         dedupTags(recipe.Tags),
	})
	if err != nil {
		return 0, fmt.Errorf("recipes: save: %w", err)
	}

	for i, ing := range recipe.Ingredients {
		name, qty, unit := ing.Name, ing.Quantity, ing.Unit
		if name == "" {
			name, qty, unit = splitIngredient(ing.Raw)
		}
		if err := store.AddCatalogRecipeIngredient(ctx, cr.ID, name, qty, unit, i); err != nil {
			return cr.ID, fmt.Errorf("recipes: ingredient %d: %w", i, err)
		}
	}

	for i, step := range recipe.Steps {
		if err := store.AddCatalogRecipeStep(ctx, cr.ID, i, step); err != nil {
			return cr.ID, fmt.Errorf("recipes: step %d: %w", i, err)
		}
	}

	return cr.ID, nil
}

// Refresh re-fetches an already-imported recipe's source page and replaces its
// ingredients, steps, timing, and (when missing) its image with freshly parsed
// data. Used by the "re-import" button to repair recipes saved before the
// JSON-LD parser handled array-wrapped documents.
func Refresh(ctx context.Context, store db.Store, id int64, imageDir string) error {
	cr, err := store.GetCatalogRecipe(ctx, id)
	if err != nil {
		return err
	}
	if cr == nil {
		return fmt.Errorf("recipes: recipe %d not found", id)
	}
	if cr.SourceURL == "" {
		return fmt.Errorf("recipes: %q has no source URL to re-import from", cr.Title)
	}
	if video.IsVideoURL(cr.SourceURL) {
		return fmt.Errorf("recipes: re-import isn't available for video recipes yet - import the link again instead")
	}

	res, err := safefetch.Fetch(ctx, cr.SourceURL, nil)
	if err != nil {
		return fmt.Errorf("recipes: fetch %q: %w", cr.SourceURL, err)
	}
	recipe, err := scrape.ParseRecipe(string(res.Body), res.FinalURL)
	if err != nil {
		return fmt.Errorf("recipes: parse: %w", err)
	}
	if len(recipe.Ingredients) == 0 && len(recipe.Steps) == 0 {
		return fmt.Errorf("recipes: no ingredients or steps found at %s", cr.SourceURL)
	}

	title := cr.Title
	if recipe.Title != "" {
		title = recipe.Title
	}
	imagePath := ""
	if cr.ImagePath == "" && recipe.ImageURL != "" && imageDir != "" {
		imagePath = downloadImage(ctx, recipe.ImageURL, imageDir)
	}

	if err := store.UpdateCatalogRecipe(ctx, db.UpdateCatalogRecipeParams{
		ID:          cr.ID,
		Title:       title,
		Servings:    pickInt(recipe.Servings, cr.Servings),
		PrepMinutes: pickInt(recipe.PrepMinutes, cr.PrepMinutes),
		CookMinutes: pickInt(recipe.CookMinutes, cr.CookMinutes),
		Tags:        dedupTags(append(cr.Tags, recipe.Tags...)),
		ImagePath:   imagePath,
	}); err != nil {
		return err
	}
	return ReplaceContent(ctx, store, cr.ID, rawIngredients(recipe), recipe.Steps)
}

// ReplaceContent swaps a recipe's ingredient and step lists for the given ones.
func ReplaceContent(ctx context.Context, store db.Store, id int64, ingredients, steps []string) error {
	if err := store.DeleteCatalogRecipeIngredients(ctx, id); err != nil {
		return err
	}
	if err := store.DeleteCatalogRecipeSteps(ctx, id); err != nil {
		return err
	}
	pos := 0
	for _, raw := range ingredients {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		name, qty, unit := splitIngredient(raw)
		if err := store.AddCatalogRecipeIngredient(ctx, id, name, qty, unit, pos); err != nil {
			return err
		}
		pos++
	}
	pos = 0
	for _, step := range steps {
		if strings.TrimSpace(step) == "" {
			continue
		}
		if err := store.AddCatalogRecipeStep(ctx, id, pos, strings.TrimSpace(step)); err != nil {
			return err
		}
		pos++
	}
	return nil
}

// DownloadImage fetches an image URL into imageDir and returns the stored
// filename, or an error when the fetch or write fails.
func DownloadImage(ctx context.Context, imageURL, imageDir string) (string, error) {
	if imageDir == "" {
		return "", fmt.Errorf("recipes: RECIPE_IMAGE_DIR is not configured")
	}
	name := downloadImage(ctx, imageURL, imageDir)
	if name == "" {
		return "", fmt.Errorf("recipes: could not download %s", imageURL)
	}
	return name, nil
}

// SaveImageBytes writes uploaded image bytes to imageDir under a fresh name
// and returns that name.
func SaveImageBytes(imageDir, origName string, data []byte) (string, error) {
	if imageDir == "" {
		return "", fmt.Errorf("recipes: RECIPE_IMAGE_DIR is not configured")
	}
	ext := strings.ToLower(filepath.Ext(origName))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
	default:
		return "", fmt.Errorf("recipes: unsupported image type %q", ext)
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return "", err
	}
	name := uuid.NewString() + ext
	if err := os.WriteFile(filepath.Join(imageDir, name), data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// RemoveImage deletes a stored image file, ignoring a missing file.
func RemoveImage(imageDir, name string) {
	if imageDir == "" || name == "" || strings.ContainsAny(name, `/\`) {
		return
	}
	_ = os.Remove(filepath.Join(imageDir, name))
}

func rawIngredients(r *scrape.Recipe) []string {
	out := make([]string, 0, len(r.Ingredients))
	for _, ing := range r.Ingredients {
		out = append(out, ing.Raw)
	}
	return out
}

func pickInt(fresh, existing int) int {
	if fresh > 0 {
		return fresh
	}
	return existing
}

// SaveManual saves a manually-entered recipe to the catalog.
func SaveManual(ctx context.Context, store db.Store, householdID int64, p db.CreateCatalogRecipeParams,
	ingredients []string, steps []string) (int64, error) {
	p.SourceKind = "manual"
	cr, err := store.CreateCatalogRecipe(ctx, p)
	if err != nil {
		return 0, err
	}
	for i, raw := range ingredients {
		name, qty, unit := splitIngredient(raw)
		if err := store.AddCatalogRecipeIngredient(ctx, cr.ID, name, qty, unit, i); err != nil {
			return cr.ID, err
		}
	}
	for i, step := range steps {
		if err := store.AddCatalogRecipeStep(ctx, cr.ID, i, step); err != nil {
			return cr.ID, err
		}
	}
	return cr.ID, nil
}

// downloadImage fetches an image URL and writes it to imageDir/<uuid>.<ext>.
// Returns the relative path on success, empty string on any failure.
func downloadImage(ctx context.Context, imageURL, imageDir string) string {
	res, err := safefetch.Fetch(ctx, imageURL, &safefetch.Options{MaxBytes: 8 * 1024 * 1024})
	if err != nil {
		return ""
	}
	ext := imageExt(imageURL, res.ContentType)
	name := uuid.NewString() + ext
	path := filepath.Join(imageDir, name)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return ""
	}
	f, err := os.Create(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Write(res.Body); err != nil {
		_ = os.Remove(path)
		return ""
	}
	return name
}

func imageExt(rawURL, contentType string) string {
	ct := strings.ToLower(strings.Split(contentType, ";")[0])
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	// Fall back to URL extension.
	if u, err := url.Parse(rawURL); err == nil {
		if ext := filepath.Ext(u.Path); ext != "" {
			return ext
		}
	}
	return ".jpg"
}

func dedupTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// knownUnits are the words splitIngredient (and a video import's model
// output) treat as a unit when they follow the quantity.
var knownUnits = map[string]bool{
	"cup": true, "cups": true, "tbsp": true, "tsp": true,
	"tablespoon": true, "tablespoons": true, "teaspoon": true, "teaspoons": true,
	"oz": true, "ounce": true, "ounces": true,
	"lb": true, "lbs": true, "pound": true, "pounds": true,
	"g": true, "gram": true, "grams": true,
	"kg": true, "ml": true, "l": true, "liter": true, "liters": true,
	"clove": true, "cloves": true, "bunch": true, "pinch": true,
	"can": true, "cans": true, "slice": true, "slices": true,
	"piece": true, "pieces": true, "whole": true, "large": true,
	"medium": true, "small": true,
}

// splitIngredient does a best-effort parse of a raw ingredient string into
// (name, quantity, unit). It is intentionally simple: first token(s) that
// are numeric become quantity, next word that looks like a unit becomes unit,
// the rest is name.
func splitIngredient(raw string) (name, quantity, unit string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw, "", ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 1 {
		return raw, "", ""
	}

	i := 0
	// Consume numeric tokens.
	var qparts []string
	for i < len(parts) {
		p := strings.Trim(parts[i], ".,/")
		if isNumericLike(p) {
			qparts = append(qparts, parts[i])
			i++
		} else {
			break
		}
	}
	quantity = strings.Join(qparts, " ")

	// Next token: unit?
	if i < len(parts) && knownUnits[strings.ToLower(strings.Trim(parts[i], ".,"))] {
		unit = parts[i]
		i++
	}

	name = strings.Join(parts[i:], " ")
	if name == "" {
		name = raw
		quantity = ""
		unit = ""
	}
	return name, quantity, unit
}

func isNumericLike(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '/' && r != '½' && r != '¼' && r != '¾' {
			return false
		}
	}
	return true
}
