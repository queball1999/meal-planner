package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"goeat/db"
)

// CostResult summarizes the outcome of pricing a full plan.
type CostResult struct {
	TotalCents        int64
	ConfidenceSummary string // e.g. "78% from live/cached prices, 22% estimated"
}

// CostPlan prices all ingredients in a plan against the given stores using the
// resolution chain, writes shopping_list_items, updates plan.total_cents and
// plan.confidence_summary, then returns a CostResult (§6.4, §7.3).
//
// stores is ordered by preference; the first store that answers for an item wins.
// If no store answers, the item is priced at zero and tagged "estimate".
func CostPlan(
	ctx context.Context,
	store db.Store,
	chain *Chain,
	planID int64,
	household *db.Household,
	stores []*db.GroceryStore,
) (*CostResult, error) {
	ingredients, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list ingredients: %w", err)
	}

	// Clear any previous shopping list for this plan (idempotent re-price).
	if err := store.DeleteShoppingListItems(ctx, planID); err != nil {
		return nil, fmt.Errorf("clear shopping list: %w", err)
	}

	items, err := AggregateByItem(ctx, store, household.ID, ingredients)
	if err != nil {
		return nil, fmt.Errorf("aggregate ingredients: %w", err)
	}

	// Subtract what the household already has, before anything is priced -
	// see ApplyPantry. Non-fatal: a plan that over-buys is worse than one that
	// does not, but it is still a usable plan, and losing the whole costing
	// run over a pantry read would be a bad trade.
	deducted, perr := ApplyPantry(ctx, store, household.ID, items)
	if perr != nil {
		log.Printf("costing: pantry deduction: %v", perr)
	}

	region := household.ZIPCode

	var totalCents int64
	confidenceCounts := map[string]int{}

	for idx, item := range items {
		var priceCents int64
		var packSize float64 = 1
		var purchaseUnit, priceSource, confidence string
		var resolvedStoreID *int64
		priced := false

		// 1. Prefer a per-store package for a catalogued item: it carries the
		//    real "amount per package" in a known unit, so pack maths is exact.
		if item.ItemID != nil {
			conv, _ := store.ListConversionsForItem(ctx, *item.ItemID)
			for _, gs := range stores {
				pkg, perr := store.GetItemStorePackage(ctx, *item.ItemID, gs.ID)
				if perr != nil {
					log.Printf("costing: package lookup: %v", perr)
					continue
				}
				if pkg == nil {
					continue
				}
				packStock, okc := Convert(pkg.AmountPerPackage, pkg.PurchaseUnit, item.Unit, conv)
				if !okc || packStock <= 0 {
					packStock = pkg.AmountPerPackage // best effort; still better than nothing
				}
				packSize = packStock
				purchaseUnit = pkg.PurchaseUnit
				priceCents = pkg.PriceCents
				priceSource = "manual"
				confidence = ConfidenceManual
				sid := gs.ID
				resolvedStoreID = &sid
				priced = true
				break
			}
		}

		// 2. Fall back to the resolution chain keyed by normalized term.
		if !priced {
			for _, gs := range stores {
				r, rerr := chain.Resolve(ctx, item.NormalizedTerm, gs.ID, region)
				if rerr != nil {
					log.Printf("costing: %v", rerr)
					continue
				}
				if r != nil {
					packSize = r.PackSize
					purchaseUnit = r.PurchaseUnit
					priceCents = r.PriceCents
					priceSource = r.Source
					confidence = r.Confidence
					sid := gs.ID
					resolvedStoreID = &sid
					priced = true
					break
				}
			}
		}

		// 3. Last resort: the meal-planning LLM's own price guess, captured per
		// ingredient at generation time (plan.systemPrompt requires it). Used
		// only when nothing above - including a fresh AI estimate in the chain
		// above - could price the item, e.g. no stores configured, every
		// provider errored, or the LLM provider was since removed.
		if !priced && item.EstPriceCents > 0 {
			packSize = item.TotalQuantity
			purchaseUnit = item.Unit
			priceCents = item.EstPriceCents
			priceSource = "estimate"
			confidence = ConfidenceEstimate
			priced = true
		}

		var buyQuantity float64
		var lineTotal int64
		if priced {
			packs := PacksNeeded(item.TotalQuantity, packSize)
			buyQuantity = float64(packs) * packSize
			lineTotal = priceCents * int64(packs)
		} else {
			// Truly nothing to go on: no live price, no chain estimate, and no
			// initial LLM guess either. Show the recipe's own quantity/unit
			// instead of rounding up to whole units in a blank purchase unit.
			priceSource = "estimate"
			confidence = ConfidenceEstimate
			purchaseUnit = item.Unit
			packSize = item.TotalQuantity
			buyQuantity = item.TotalQuantity
		}
		// A line the pantry covered entirely stays on the list but is not
		// bought, so it must not reach the total either - the same rule the
		// "I already have this" control follows.
		ded := deducted[idx]
		if !ded.Covered {
			totalCents += lineTotal
		}
		confidenceCounts[confidence]++

		refsJSON, _ := json.Marshal(item.IngredientIDs)
		_, cerr := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID:             planID,
			StoreID:            resolvedStoreID,
			ItemID:             item.ItemID,
			MealIngredientRefs: string(refsJSON),
			DisplayName:        item.DisplayName,
			BuyQuantity:        buyQuantity,
			PackSize:           packSize,
			PurchaseUnit:       purchaseUnit,
			UnitPriceCents:     priceCents,
			LineTotalCents:     lineTotal,
			PriceSource:        priceSource,
			Confidence:         confidence,
			PantryQtyUsed:      ded.Used,
			InPantry:           ded.Covered,
		})
		if cerr != nil {
			log.Printf("costing: create shopping list item %q: %v", item.DisplayName, cerr)
		}
	}

	summary := BuildConfidenceSummary(confidenceCounts, len(items))

	if err := store.UpdatePlanTotal(ctx, planID, totalCents, summary); err != nil {
		return nil, fmt.Errorf("update plan total: %w", err)
	}

	return &CostResult{
		TotalCents:        totalCents,
		ConfidenceSummary: summary,
	}, nil
}

// BuildConfidenceSummary renders the plan-level "78% from live/cached prices,
// 22% estimated" line from a tally of shopping-list line confidences.
func BuildConfidenceSummary(counts map[string]int, total int) string {
	if total == 0 {
		return "no items"
	}
	live := counts[ConfidenceLive] + counts[ConfidenceCached] + counts[ConfidenceManual] + counts[ConfidenceScrape]
	est := counts[ConfidenceEstimate]
	livePct := live * 100 / total
	estPct := est * 100 / total
	if estPct == 0 {
		return fmt.Sprintf("100%% from live/cached prices")
	}
	if livePct == 0 {
		return fmt.Sprintf("100%% estimated")
	}
	return fmt.Sprintf("%d%% from live/cached prices, %d%% estimated", livePct, estPct)
}

// EnsureShoppingList guarantees a plan has a shopping list, whatever happened
// during pricing. CostPlan is the normal path and writes a fully priced list,
// but it is optional (no pricing chain configured) and its failures are
// non-fatal, which used to leave a finished plan with an empty list and no way
// to shop it. This runs after pricing: if the list already has lines it is a
// no-op, otherwise it writes one unpriced line per aggregated ingredient using
// the plan LLM's own price guess where there is one.
//
// Returns the number of lines it created (0 when the list was already there).
func EnsureShoppingList(ctx context.Context, store db.Store, planID int64, household *db.Household) (int, error) {
	existing, err := store.ListShoppingListItems(ctx, planID)
	if err != nil {
		return 0, fmt.Errorf("list shopping items: %w", err)
	}
	if len(existing) > 0 {
		return 0, nil
	}

	ingredients, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		return 0, fmt.Errorf("list ingredients: %w", err)
	}
	if len(ingredients) == 0 {
		return 0, nil
	}

	items, err := AggregateByItem(ctx, store, household.ID, ingredients)
	if err != nil {
		return 0, fmt.Errorf("aggregate ingredients: %w", err)
	}

	var written int
	var totalCents int64
	for _, item := range items {
		refsJSON, _ := json.Marshal(item.IngredientIDs)
		// No pack maths is possible without a resolved package, so the line
		// buys exactly the recipe quantity and carries the LLM's guess (if any)
		// as the line total.
		lineTotal := item.EstPriceCents
		totalCents += lineTotal
		if _, cerr := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID:             planID,
			ItemID:             item.ItemID,
			MealIngredientRefs: string(refsJSON),
			DisplayName:        item.DisplayName,
			BuyQuantity:        item.TotalQuantity,
			PackSize:           item.TotalQuantity,
			PurchaseUnit:       item.Unit,
			UnitPriceCents:     lineTotal,
			LineTotalCents:     lineTotal,
			PriceSource:        "estimate",
			Confidence:         ConfidenceEstimate,
		}); cerr != nil {
			log.Printf("costing: fallback shopping line %q: %v", item.DisplayName, cerr)
			continue
		}
		written++
	}

	if written > 0 {
		summary := BuildConfidenceSummary(map[string]int{ConfidenceEstimate: written}, written)
		if err := store.UpdatePlanTotal(ctx, planID, totalCents, summary); err != nil {
			log.Printf("costing: fallback plan total: %v", err)
		}
	}
	return written, nil
}
