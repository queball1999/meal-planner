package pricing

import (
	"context"
	"log"
	"math"

	"goeat/db"
)

// BackfillNeed fills in need_quantity (00037) on a plan's lines that were
// priced before the column existed, by re-aggregating the plan's ingredients
// the way SeedShoppingList does and matching them to lines the way rescale
// does (existingKey). Updates lines in place and persists each value, so it
// runs at most once per line. The need is gross - before pantry stock - as
// every writer records it: the pantry note says what the pantry covers.
func BackfillNeed(ctx context.Context, store db.Store, householdID, planID int64, lines []*db.ShoppingListItem) {
	var missing []*db.ShoppingListItem
	for _, ln := range lines {
		if ln.NeedQuantity <= 0 && !ln.Pending {
			missing = append(missing, ln)
		}
	}
	if len(missing) == 0 {
		return
	}

	ingredients, err := store.ListIngredientsByPlan(ctx, planID)
	if err != nil {
		log.Printf("pricing: backfill need for plan %d: %v", planID, err)
		return
	}
	items, err := AggregateByItem(ctx, store, householdID, ingredients)
	if err != nil {
		log.Printf("pricing: backfill need for plan %d: %v", planID, err)
		return
	}
	need := make(map[string]float64, len(items))
	for _, it := range items {
		need[itemKey(it.ItemID, it.NormalizedTerm)] = it.TotalQuantity
	}
	for _, ln := range missing {
		q, ok := need[existingKey(ln)]
		if !ok || q <= 0 {
			continue
		}
		if err := store.SetShoppingListItemNeed(ctx, ln.ID, q); err != nil {
			log.Printf("pricing: backfill need for line %d: %v", ln.ID, err)
			continue
		}
		ln.NeedQuantity = q
	}
}

// PacksBought is how many store packs a shopping line buys. Zero when it was
// never matched to a real package (pack_amount 0): it buys exactly what it
// needs, in its purchase unit.
func PacksBought(item *db.ShoppingListItem) int {
	if item.PackAmount <= 0 || item.PackSize <= 0 {
		return 0
	}
	n := int(math.Round(item.BuyQuantity / item.PackSize))
	if n < 1 {
		n = 1
	}
	return n
}
