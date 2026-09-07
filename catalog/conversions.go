package catalog

import (
	"context"

	"goeat/db"
	"goeat/pricing"
)

// RecalcItemConversions rebuilds an item's derived conversion rows: a direct
// edge from every unit the app knows about to the item's stock unit, plus a
// package -> stock-unit edge from its default purchase qty. Hand-entered and
// seeded edges are used as input and left in place. Idempotent - call it after
// an item's stock unit, default buy qty, or hand-entered conversions change.
func RecalcItemConversions(ctx context.Context, store db.Store, itemID int64) error {
	it, err := store.GetItem(ctx, itemID)
	if err != nil || it == nil {
		return err
	}
	all, err := store.ListConversionsForItem(ctx, itemID)
	if err != nil {
		return err
	}
	seed := make([]*db.UnitConversion, 0, len(all))
	for _, c := range all {
		if !c.Derived {
			seed = append(seed, c)
		}
	}
	edges := pricing.AutoConversions(it.StockUnit, it.DefaultPurchaseQty, seed)
	return store.ReplaceDerivedItemConversions(ctx, itemID, edges)
}
