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
)

// Import fetches rawURL, parses a Recipe via scrape.ParseRecipe, optionally
// downloads the image, persists everything to the DB, and returns the new
// catalog recipe ID. Image download failures are non-fatal.
func Import(ctx context.Context, store db.Store, householdID int64, rawURL, imageDir string) (int64, error) {
	// 1. Fetch the page.
	res, err := safefetch.Fetch(ctx, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("recipes: fetch %q: %w", rawURL, err)
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

	// 4. Persist the recipe.
	tags := dedupTags(recipe.Tags)
	cr, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: householdID,
		Title:       recipe.Title,
		SourceKind:  "imported",
		SourceURL:   res.FinalURL,
		SourceSite:  recipe.SourceSite,
		ImagePath:   imagePath,
		Servings:    recipe.Servings,
		PrepMinutes: recipe.PrepMinutes,
		CookMinutes: recipe.CookMinutes,
		Tags:        tags,
	})
	if err != nil {
		return 0, fmt.Errorf("recipes: save: %w", err)
	}

	// 5. Persist ingredients.
	for i, ing := range recipe.Ingredients {
		name, qty, unit := splitIngredient(ing.Raw)
		if err := store.AddCatalogRecipeIngredient(ctx, cr.ID, name, qty, unit, i); err != nil {
			return cr.ID, fmt.Errorf("recipes: ingredient %d: %w", i, err)
		}
	}

	// 6. Persist steps.
	for i, step := range recipe.Steps {
		if err := store.AddCatalogRecipeStep(ctx, cr.ID, i, step); err != nil {
			return cr.ID, fmt.Errorf("recipes: step %d: %w", i, err)
		}
	}

	return cr.ID, nil
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

	units := map[string]bool{
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
	if i < len(parts) && units[strings.ToLower(strings.Trim(parts[i], ".,"))] {
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
