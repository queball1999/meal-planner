package pricing

import (
	"context"
	"fmt"
	"log"
	"math"

	"goeat/db"
)

// RescaleShoppingList re-syncs a plan's shopping list after a change that
// only alters *how much* of each ingredient is needed - a guest count change
// or a meal skip/eating-out toggle - rather than what the plan itself
// contains. Unlike CostPlan (which wipes the list and re-resolves every
// line), a line that is already priced keeps its resolved store/price/source
// and is only rescaled to the new quantity; nothing is asked of a provider
// (cache, scraper, or AI) for it. Only a line that is new (a meal came back)
// or was never priced gets the normal seed-and-resolve treatment.
func RescaleShoppingList(
	ctx context.Context,
	store db.Store,
	chain *Chain,
	planID int64,
	household *db.Household,
	stores []*db.GroceryStore,
) (*CostResult, error) {
	existing, err := store.ListShoppingListItems(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list shopping items: %w", err)
	}

	ingredients, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list ingredients: %w", err)
	}
	if len(ingredients) == 0 {
		for _, e := range existing {
			if derr := store.DeleteShoppingListItem(ctx, e.ID); derr != nil {
				log.Printf("pricing: rescale: delete %d: %v", e.ID, derr)
			}
		}
		if err := store.UpdatePlanTotal(ctx, planID, 0, "no items"); err != nil {
			return nil, fmt.Errorf("update plan total: %w", err)
		}
		return &CostResult{}, nil
	}

	items, err := AggregateByItem(ctx, store, household.ID, ingredients)
	if err != nil {
		return nil, fmt.Errorf("aggregate ingredients: %w", err)
	}

	deducted, perr := ApplyPantry(ctx, store, household.ID, items)
	if perr != nil {
		log.Printf("costing: pantry deduction: %v", perr)
	}

	exByKey := indexExistingItems(existing)

	rescaled := make(map[int64]bool, len(existing))
	var toSeed []AggItem
	toSeedDed := map[int]PantryDeduction{}

	for idx, item := range items {
		ded := deducted[idx]
		ex, ok := exByKey[itemKey(item.ItemID, item.NormalizedTerm)]
		if ok && !ex.Pending && ex.UnitPriceCents > 0 && rescaleInPlace(ctx, store, ex, item, ded) {
			rescaled[ex.ID] = true
			continue
		}
		toSeedDed[len(toSeed)] = ded
		toSeed = append(toSeed, item)
	}

	// Clear every existing row not rescaled in place: it is either stale (the
	// ingredient it priced is no longer needed) or about to be recreated by
	// the seed-and-resolve pass below - either way it must not linger.
	for _, e := range existing {
		if rescaled[e.ID] {
			continue
		}
		if derr := store.DeleteShoppingListItem(ctx, e.ID); derr != nil {
			log.Printf("pricing: rescale: delete stale item %d: %v", e.ID, derr)
		}
	}

	if len(toSeed) > 0 {
		seeded, serr := seedItems(ctx, store, planID, toSeed, toSeedDed)
		if serr != nil {
			return nil, serr
		}
		if _, rerr := ResolvePricing(ctx, store, chain, planID, household, stores, seeded); rerr != nil {
			return nil, rerr
		}
	}

	return recomputePlanTotal(ctx, store, planID)
}

// itemKey is the matching key between an aggregated ingredient line and an
// existing shopping-list row: the catalog item when there is one, otherwise
// the normalized term. Two different plans never share a shopping list, so
// this only has to disambiguate lines within one plan.
func itemKey(itemID *int64, normalizedTerm string) string {
	if itemID != nil {
		return fmt.Sprintf("#%d", *itemID)
	}
	return "n:" + normalizedTerm
}

// indexExistingItems keys a plan's current shopping-list rows the same way
// itemKey does, so a fresh AggItem can look up whatever line already prices
// the same ingredient. A row with no catalog item is keyed by its own display
// name normalized the same way AggregateByItem's unit-naive fallback derives
// a term from an ingredient name - the two only ever agree because nothing
// else renames a line between seed and rescale.
func indexExistingItems(existing []*db.ShoppingListItem) map[string]*db.ShoppingListItem {
	out := make(map[string]*db.ShoppingListItem, len(existing))
	for _, e := range existing {
		var k string
		if e.ItemID != nil {
			k = itemKey(e.ItemID, "")
		} else {
			k = itemKey(nil, Normalize(e.DisplayName))
		}
		if _, dup := out[k]; !dup {
			out[k] = e
		}
	}
	return out
}

// rescaleInPlace rewrites one already-priced row for a new required quantity
// without asking any provider for a price. Returns false when the row can't
// be rescaled this way (no usable pack math and no positive old quantity to
// ratio against) - the caller then falls back to pricing it fresh.
func rescaleInPlace(ctx context.Context, store db.Store, ex *db.ShoppingListItem, item AggItem, ded PantryDeduction) bool {
	var buyQuantity, packSize float64
	var lineTotal int64
	handled := false

	// Preferred: the line was resolved against a real discrete package
	// (pack_amount/pack_unit, 00030) - redo the same pack math for the new
	// required quantity, at the same per-pack price.
	if ex.PackAmount > 0 {
		conv := loadConversions(ctx, store, item.ItemID)
		packs, bq, ps, reconciled := reconcilePack(item.TotalQuantity, ex.PackAmount, ex.PackUnit, item.Unit, conv)
		if reconciled {
			buyQuantity, packSize = bq, ps
			lineTotal = ex.UnitPriceCents * int64(packs)
			handled = true
		}
	}

	// Fallback: the line was priced flatly for its old exact quantity (no
	// real pack, or the conversion graph changed since) - scale that flat
	// price by how much the required quantity itself changed.
	if !handled {
		if ex.BuyQuantity <= 0 {
			return false
		}
		ratio := item.TotalQuantity / ex.BuyQuantity
		buyQuantity = item.TotalQuantity
		packSize = item.TotalQuantity
		lineTotal = int64(math.Round(float64(ex.LineTotalCents) * ratio))
	}

	if err := store.UpdateShoppingListItemQuantity(ctx, ex.ID, buyQuantity, packSize, lineTotal, ded.Used, ded.Covered); err != nil {
		log.Printf("pricing: rescale item %d: %v", ex.ID, err)
		return false
	}
	return true
}

// recomputePlanTotal re-reads a plan's shopping list and writes its total and
// confidence summary from what is actually stored, rather than threading a
// running total through both the in-place rescale pass and the fresh-pricing
// pass above - simpler, and correct by construction however the two split.
func recomputePlanTotal(ctx context.Context, store db.Store, planID int64) (*CostResult, error) {
	items, err := store.ListShoppingListItems(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list shopping items: %w", err)
	}

	var totalCents int64
	confidenceCounts := map[string]int{}
	for _, it := range items {
		if !it.InPantry {
			totalCents += it.LineTotalCents
		}
		confidenceCounts[it.Confidence]++
	}

	summary := BuildConfidenceSummary(confidenceCounts, len(items))
	if err := store.UpdatePlanTotal(ctx, planID, totalCents, summary); err != nil {
		return nil, fmt.Errorf("update plan total: %w", err)
	}
	return &CostResult{TotalCents: totalCents, ConfidenceSummary: summary}, nil
}
