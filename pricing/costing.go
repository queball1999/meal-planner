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

	items := AggregateIngredients(ingredients)
	region := household.ZIPCode

	var totalCents int64
	confidenceCounts := map[string]int{}

	for _, item := range items {
		var result *PriceResult
		var resolvedStoreID *int64

		// Try each store in order; stop at the first answer.
		for _, gs := range stores {
			r, err := chain.Resolve(ctx, item.NormalizedTerm, gs.ID, region)
			if err != nil {
				log.Printf("costing: %v", err)
				continue
			}
			if r != nil {
				result = r
				sid := gs.ID
				resolvedStoreID = &sid
				break
			}
		}

		var priceCents int64
		var packSize float64 = 1
		var purchaseUnit, priceSource, confidence string

		if result != nil {
			packSize = result.PackSize
			purchaseUnit = result.PurchaseUnit
			priceCents = result.PriceCents
			priceSource = result.Source
			confidence = result.Confidence
		} else {
			priceSource = "estimate"
			confidence = ConfidenceEstimate
		}

		packs := PacksNeeded(item.TotalQuantity, packSize)
		lineTotal := priceCents * int64(packs)
		totalCents += lineTotal
		confidenceCounts[confidence]++

		refsJSON, _ := json.Marshal(item.IngredientIDs)
		_, err := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
			PlanID:             planID,
			StoreID:            resolvedStoreID,
			MealIngredientRefs: string(refsJSON),
			DisplayName:        item.DisplayName,
			BuyQuantity:        float64(packs) * packSize,
			PackSize:           packSize,
			PurchaseUnit:       purchaseUnit,
			UnitPriceCents:     priceCents,
			LineTotalCents:     lineTotal,
			PriceSource:        priceSource,
			Confidence:         confidence,
		})
		if err != nil {
			log.Printf("costing: create shopping list item %q: %v", item.DisplayName, err)
		}
	}

	summary := buildConfidenceSummary(confidenceCounts, len(items))

	if err := store.UpdatePlanTotal(ctx, planID, totalCents, summary); err != nil {
		return nil, fmt.Errorf("update plan total: %w", err)
	}

	return &CostResult{
		TotalCents:        totalCents,
		ConfidenceSummary: summary,
	}, nil
}

func buildConfidenceSummary(counts map[string]int, total int) string {
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
