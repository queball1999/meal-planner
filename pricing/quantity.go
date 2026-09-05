package pricing

import (
	"math"
	"strings"

	"goeat/db"
)

// AggItem is one ingredient term aggregated across all meals in a plan.
type AggItem struct {
	NormalizedTerm string
	DisplayName    string  // original name from the first ingredient seen
	TotalQuantity  float64 // sum of quantities in the ingredient's own unit
	Unit           string
	IngredientIDs  []int64 // source meal_ingredient IDs for shopping_list_items refs
}

// AggregateIngredients sums ingredient quantities by normalized term across
// all meals in a plan. Ingredients with the same normalized_term and unit are
// summed; mismatched units are kept separate (§6.4).
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

// PacksNeeded computes how many whole packs must be bought to cover need units
// given packSize units per pack (§6.4). Always rounds up (ceil).
func PacksNeeded(need, packSize float64) int {
	if packSize <= 0 {
		packSize = 1
	}
	return int(math.Ceil(need / packSize))
}
