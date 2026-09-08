// Package catalog links free-text ingredient names to the household's items
// table (the canonical grocery-item catalog) and seeds the starter set. It sits
// above db and pricing so it can normalize terms without an import cycle.
package catalog

import (
	"context"
	"log"
	"strings"

	"goeat/db"
	"goeat/pricing"
)

// ItemHint carries what a recipe generator knows about an ingredient's catalog
// item beyond its name: the unit it is stocked and bought in, and any
// item-specific unit conversions ("1 clove garlic ≈ 5 g") the builtin
// metric/imperial graph cannot know on its own. Every field is optional.
type ItemHint struct {
	Unit        string
	Conversions []UnitEdge
}

// UnitEdge is one directed conversion supplied by a hint: 1 From = Factor To.
type UnitEdge struct {
	From, To string
	Factor   float64
}

func (h ItemHint) hasContent() bool {
	return strings.TrimSpace(h.Unit) != "" || len(h.Conversions) > 0
}

// EnsureItem returns the household's catalog item for name, in three steps:
//
//  1. Exact match on the normalized term.
//  2. The alias table - a term the household has already said means an
//     existing item (00020_item_aliases.sql).
//  3. A fuzzy match against the real catalog, taken automatically only when it
//     scores above AutoLinkScore.
//
// Failing all three it creates a source='auto' placeholder, which is what the
// shopping list flags yellow for a person to resolve. A name that normalizes
// to empty yields (nil, nil) - the caller should just skip linking.
//
// Step 3 is why this is not simply "look up or create": before aliases,
// "chicken breast" and "chicken breasts" became two items with two prices and
// two pantry stocks, and nothing ever noticed.
func EnsureItem(ctx context.Context, store db.Store, householdID int64, name string) (*db.Item, error) {
	return EnsureItemWithHint(ctx, store, householdID, name, ItemHint{})
}

// EnsureItemWithHint is EnsureItem plus the recipe generator's knowledge of the
// item (see ItemHint). A brand-new item takes the hint's canonical unit as its
// stock unit (instead of the "each" default) and, either way, has its derived
// one-hop conversion table built immediately so costing and the item page never
// BFS at read time. Hint conversions are written as item-specific edges, but
// only where the item does not already have that pair - a hand-entered
// conversion is never overwritten by the model. For an item that already
// exists, the hint only enriches conversions; its stock unit is left as set.
func EnsureItemWithHint(ctx context.Context, store db.Store, householdID int64, name string, hint ItemHint) (*db.Item, error) {
	term := pricing.Normalize(name)
	if term == "" {
		return nil, nil
	}
	if it, err := store.GetItemByTerm(ctx, householdID, term); err != nil {
		return nil, err
	} else if it != nil {
		return enrichItem(ctx, store, it, hint, false)
	}
	if it, err := store.GetItemByAlias(ctx, householdID, term); err != nil {
		return nil, err
	} else if it != nil {
		return enrichItem(ctx, store, it, hint, false)
	}

	// A fuzzy match confident enough to take without asking is recorded as an
	// alias, so the next sighting of this name resolves at step 2 instead of
	// re-scoring the whole catalog.
	if it, err := autoMatch(ctx, store, householdID, name, term); err != nil {
		return nil, err
	} else if it != nil {
		return enrichItem(ctx, store, it, hint, false)
	}

	stockUnit := "each"
	if u := pricing.CanonUnit(hint.Unit); u != "" {
		stockUnit = u
	}
	it, err := store.CreateItem(ctx, db.CreateItemParams{
		HouseholdID:        householdID,
		Name:               strings.TrimSpace(name),
		NormalizedTerm:     term,
		StockUnit:          stockUnit,
		DefaultPurchaseQty: 1,
		Source:             "auto",
	})
	if err != nil || it == nil {
		return it, err
	}
	return enrichItem(ctx, store, it, hint, true)
}

// enrichItem applies a hint's conversion edges to an item (never clobbering an
// existing pair) and rebuilds its derived conversion table. isNew forces the
// rebuild even with an empty hint, so every freshly created item gets its
// conversions precomputed the moment its unit is known. All failures are
// logged, not fatal: a missing conversion only downgrades a shopping line to an
// estimate, and losing a whole plan generation over it would be absurd.
func enrichItem(ctx context.Context, store db.Store, it *db.Item, hint ItemHint, isNew bool) (*db.Item, error) {
	if !isNew && !hint.hasContent() {
		return it, nil
	}

	if len(hint.Conversions) > 0 {
		existing, _ := store.ListConversionsForItem(ctx, it.ID)
		have := map[string]bool{}
		for _, c := range existing {
			if c.ItemID == nil {
				continue // global edge - a hint may still specialise it per item
			}
			f, t := pricing.CanonUnit(c.FromUnit), pricing.CanonUnit(c.ToUnit)
			have[f+">"+t] = true
			have[t+">"+f] = true
		}
		for _, e := range hint.Conversions {
			from, to := pricing.CanonUnit(e.From), pricing.CanonUnit(e.To)
			if from == "" || to == "" || from == to || e.Factor <= 0 || e.Factor > 100000 {
				continue
			}
			if have[from+">"+to] {
				continue
			}
			id := it.ID
			if err := store.UpsertUnitConversion(ctx, db.UpsertUnitConversionParams{
				ItemID: &id, FromUnit: from, ToUnit: to, Factor: e.Factor,
			}); err != nil {
				log.Printf("catalog: hint conversion %s->%s for item %d: %v", from, to, it.ID, err)
				continue
			}
			have[from+">"+to] = true
			have[to+">"+from] = true
		}
	}

	if err := RecalcItemConversions(ctx, store, it.ID); err != nil {
		log.Printf("catalog: recalc conversions for item %d: %v", it.ID, err)
	}
	return it, nil
}

// autoMatch returns the one catalog item that clearly means this name, or nil
// when nothing is confident enough to link without a person looking.
func autoMatch(ctx context.Context, store db.Store, householdID int64, name, term string) (*db.Item, error) {
	items, err := store.ListItems(ctx, householdID)
	if err != nil {
		return nil, err
	}
	best := SuggestItems(name, items, 1)
	if len(best) == 0 || best[0].Score < AutoLinkScore {
		return nil, nil
	}
	if err := store.CreateItemAlias(ctx, householdID, best[0].ItemID, term, "auto"); err != nil {
		return nil, err
	}
	return store.GetItem(ctx, best[0].ItemID)
}

// LinkPlanIngredients ensures a catalog item for every unlinked ingredient row
// in a plan and writes back item_id + normalized_term. Idempotent: rows that
// already carry an item_id are left alone. Call it right after a plan is
// persisted, before pricing.
func LinkPlanIngredients(ctx context.Context, store db.Store, householdID, planID int64) error {
	ings, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return err
	}
	for _, ing := range ings {
		if ing.ItemID != nil {
			continue
		}
		it, err := EnsureItem(ctx, store, householdID, ing.Name)
		if err != nil {
			return err
		}
		if it == nil {
			continue
		}
		id := it.ID
		if err := store.SetMealIngredientItem(ctx, ing.ID, &id, it.NormalizedTerm); err != nil {
			return err
		}
	}
	return nil
}

// BackfillHousehold links every unlinked meal-ingredient and pantry row for a
// household to a catalog item. Run once at boot so existing data picks up the
// item_id columns added in 00010_items.sql. shopping_list_items are rebuilt on
// the next re-cost, so they are not touched here.
func BackfillHousehold(ctx context.Context, store db.Store, householdID int64) error {
	plans, err := store.ListPlans(ctx, householdID)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if err := LinkPlanIngredients(ctx, store, householdID, p.ID); err != nil {
			return err
		}
	}

	pantry, err := store.ListPantryItems(ctx, householdID)
	if err != nil {
		return err
	}
	for _, pi := range pantry {
		if pi.ItemID != nil {
			continue
		}
		it, err := EnsureItem(ctx, store, householdID, pi.Name)
		if err != nil {
			return err
		}
		if it == nil {
			continue
		}
		id := it.ID
		if err := store.SetPantryItemItem(ctx, pi.ID, &id); err != nil {
			return err
		}
	}
	return nil
}
