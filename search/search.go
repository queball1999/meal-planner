// Package search provides unified search across recipe catalog, pantry, and meal history (§8.4c).
package search

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"goeat/db"
)

// Result is one search hit.
type Result struct {
	Kind  string // "recipe" | "pantry" | "meal"
	ID    int64
	Title string
	Extra string // subtitle: source site, slot+date, qty+unit
	URL   string // destination href
}

// Options controls matching behaviour.
type Options struct {
	Partial bool // use LIKE '%q%'; true by default in Search helper
	Fuzzy   bool // normalise query before matching (lowercase + strip punctuation)
	Limit   int  // per-kind cap; 0 → 10
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9 ]+`)

func normalise(s string) string {
	return strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToLower(s), " "))
}

// Search returns grouped results across catalog recipes, pantry items, and
// meal titles. If exactly one result has a title that exactly matches q, it
// is returned alone with Kind=="exact" so the handler can redirect.
func Search(ctx context.Context, store db.Store, householdID int64, q string, opts Options) ([]Result, bool, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, false, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	lookup := q
	if opts.Fuzzy {
		lookup = normalise(q)
	}

	var results []Result

	// ── Recipes ──────────────────────────────────────────────────────────────
	recipes, err := store.SearchCatalogRecipes(ctx, householdID, lookup)
	if err != nil {
		return nil, false, fmt.Errorf("search recipes: %w", err)
	}
	for _, r := range recipes {
		extra := r.SourceSite
		if extra == "" {
			extra = r.SourceKind
		}
		results = append(results, Result{
			Kind:  "recipe",
			ID:    r.ID,
			Title: r.Title,
			Extra: extra,
			URL:   fmt.Sprintf("/recipes/%d", r.ID),
		})
	}

	// ── Pantry ───────────────────────────────────────────────────────────────
	pantry, err := store.SearchPantryItems(ctx, householdID, lookup)
	if err != nil {
		return nil, false, fmt.Errorf("search pantry: %w", err)
	}
	for _, p := range pantry {
		results = append(results, Result{
			Kind:  "pantry",
			ID:    p.ID,
			Title: p.Name,
			Extra: fmt.Sprintf("%.2g %s", p.QuantityOnHand, p.Unit),
			URL:   "/pantry",
		})
	}

	// ── Meals ─────────────────────────────────────────────────────────────────
	meals, err := store.SearchMealTitles(ctx, householdID, lookup)
	if err != nil {
		return nil, false, fmt.Errorf("search meals: %w", err)
	}
	for _, m := range meals {
		results = append(results, Result{
			Kind:  "meal",
			ID:    m.ID,
			Title: m.Title,
			Extra: fmt.Sprintf("%s · %s", m.Slot, m.Day),
			URL:   fmt.Sprintf("/meals/%d", m.ID),
		})
	}

	// ── Exact-match redirect check ────────────────────────────────────────────
	if len(results) == 1 && strings.EqualFold(results[0].Title, q) {
		return results, true, nil
	}

	return results, false, nil
}
