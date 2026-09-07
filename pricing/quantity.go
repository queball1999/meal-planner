package pricing

import (
	"context"
	"math"
	"strings"

	"goeat/db"
)

// AggItem is one ingredient term aggregated across all meals in a plan.
type AggItem struct {
	NormalizedTerm string
	DisplayName    string  // original name from the first ingredient seen
	TotalQuantity  float64 // sum of quantities, expressed in Unit
	Unit           string  // the unit TotalQuantity is in (an item's stock unit when ItemID is set)
	IngredientIDs  []int64 // source meal_ingredient IDs for shopping_list_items refs

	ItemID      *int64 // catalog item, when the ingredients resolved to one
	Unconverted bool   // true when quantities could not be unit-reconciled and are approximate

	// EstPriceCents sums each contributing ingredient's own price guess from
	// plan generation (db.MealIngredient.EstPriceCents), for exactly
	// TotalQuantity of Unit. Used by costing.go as a last-resort fallback when
	// no provider (including a fresh AI estimate) can price the item, instead
	// of defaulting to $0.
	EstPriceCents int64
}

// AggregateIngredients sums ingredient quantities by normalized term across all
// meals in a plan. Ingredients with the same normalized_term and unit are
// summed; mismatched units are kept separate (§6.4). This is the unit-naive
// fallback used when no catalog item backs the ingredient.
func AggregateIngredients(ingredients []*db.MealIngredient) []AggItem {
	type key struct{ term, unit string }
	index := make(map[key]int) // key → index in out
	var out []AggItem

	for _, ing := range ingredients {
		term := ing.NormalizedTerm
		if term == "" {
			term = Normalize(ing.Name)
		}
		unit := strings.ToLower(strings.TrimSpace(ing.Unit))
		k := key{term, unit}
		if i, ok := index[k]; ok {
			out[i].TotalQuantity += ing.Quantity
			out[i].IngredientIDs = append(out[i].IngredientIDs, ing.ID)
		} else {
			index[k] = len(out)
			out = append(out, AggItem{
				NormalizedTerm: term,
				DisplayName:    ing.Name,
				TotalQuantity:  ing.Quantity,
				Unit:           unit,
				IngredientIDs:  []int64{ing.ID},
			})
		}
	}
	return out
}

// AggregateByItem groups a plan's ingredients by catalog item, converting every
// quantity into that item's stock unit before summing so "2 cups rice" and
// "1 lb rice" become one buy line. Ingredients with no catalog item (or whose
// unit cannot be converted to the item's stock unit) fall back to
// normalized-term + raw-unit grouping, matching AggregateIngredients.
func AggregateByItem(ctx context.Context, store db.Store, householdID int64, ingredients []*db.MealIngredient) ([]AggItem, error) {
	type key struct {
		term string
		unit string
		item int64 // 0 when no catalog item
	}
	index := make(map[key]int)
	var out []AggItem

	itemCache := map[string]*db.Item{}        // normalized_term → item (nil = looked up, none)
	convCache := map[int64][]*db.UnitConversion{} // item id → conversion graph

	resolveItem := func(ing *db.MealIngredient, term string) *db.Item {
		if ing.ItemID != nil {
			if it, ok := itemCache["#"+term]; ok {
				return it
			}
			it, err := store.GetItem(ctx, *ing.ItemID)
			if err != nil {
				it = nil
			}
			itemCache["#"+term] = it
			return it
		}
		if it, ok := itemCache[term]; ok {
			return it
		}
		it, err := store.GetItemByTerm(ctx, householdID, term)
		if err != nil {
			it = nil
		}
		itemCache[term] = it
		return it
	}

	for _, ing := range ingredients {
		term := ing.NormalizedTerm
		if term == "" {
			term = Normalize(ing.Name)
		}
		rawUnit := strings.ToLower(strings.TrimSpace(ing.Unit))

		it := resolveItem(ing, term)

		if it != nil {
			conv, ok := convCache[it.ID]
			if !ok {
				conv, _ = store.ListConversionsForItem(ctx, it.ID)
				convCache[it.ID] = conv
			}
			qty, converted := Convert(ing.Quantity, rawUnit, it.StockUnit, conv)
			if converted {
				k := key{item: it.ID, unit: it.StockUnit}
				if i, ok := index[k]; ok {
					out[i].TotalQuantity += qty
					out[i].IngredientIDs = append(out[i].IngredientIDs, ing.ID)
					out[i].EstPriceCents += ing.EstPriceCents
				} else {
					id := it.ID
					index[k] = len(out)
					out = append(out, AggItem{
						NormalizedTerm: it.NormalizedTerm,
						DisplayName:    it.Name,
						TotalQuantity:  qty,
						Unit:           it.StockUnit,
						IngredientIDs:  []int64{ing.ID},
						ItemID:         &id,
						EstPriceCents:  ing.EstPriceCents,
					})
				}
				continue
			}
			// Item known but unit won't convert - keep the item link, group by
			// raw unit, and flag the line as approximate.
			k := key{item: it.ID, unit: rawUnit}
			if i, ok := index[k]; ok {
				out[i].TotalQuantity += ing.Quantity
				out[i].IngredientIDs = append(out[i].IngredientIDs, ing.ID)
				out[i].EstPriceCents += ing.EstPriceCents
			} else {
				id := it.ID
				index[k] = len(out)
				out = append(out, AggItem{
					NormalizedTerm: it.NormalizedTerm,
					DisplayName:    it.Name,
					TotalQuantity:  ing.Quantity,
					Unit:           rawUnit,
					IngredientIDs:  []int64{ing.ID},
					ItemID:         &id,
					Unconverted:    true,
					EstPriceCents:  ing.EstPriceCents,
				})
			}
			continue
		}

		// No catalog item: unit-naive fallback.
		k := key{term: term, unit: rawUnit}
		if i, ok := index[k]; ok {
			out[i].TotalQuantity += ing.Quantity
			out[i].IngredientIDs = append(out[i].IngredientIDs, ing.ID)
			out[i].EstPriceCents += ing.EstPriceCents
		} else {
			index[k] = len(out)
			out = append(out, AggItem{
				NormalizedTerm: term,
				DisplayName:    ing.Name,
				TotalQuantity:  ing.Quantity,
				Unit:           rawUnit,
				IngredientIDs:  []int64{ing.ID},
				EstPriceCents:  ing.EstPriceCents,
			})
		}
	}
	return out, nil
}

// PacksNeeded computes how many whole packs must be bought to cover need units
// given packSize units per pack (§6.4). Always rounds up (ceil).
func PacksNeeded(need, packSize float64) int {
	if packSize <= 0 {
		packSize = 1
	}
	return int(math.Ceil(need / packSize))
}
