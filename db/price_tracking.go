package db

import (
	"context"
	"time"
)

// ListPriceTracking returns one row per (item, store) package the household has
// priced, joined to the store name, for the finance "price tracking" table.
// Each row carries the item's recent price history (oldest first, capped) so
// the template can draw a sparkline without a second round trip per item.
func (s *store) ListPriceTracking(ctx context.Context, householdID int64) ([]*PriceTrackingRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.item_id, i.name, p.store_id, st.name,
		       p.price_cents, p.purchase_unit, p.amount_per_package, p.preferred, p.updated_at
		FROM item_store_packages p
		JOIN items i ON i.id = p.item_id
		JOIN stores st ON st.id = p.store_id
		WHERE i.household_id = ?
		ORDER BY i.name ASC, st.name ASC`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PriceTrackingRow
	for rows.Next() {
		var r PriceTrackingRow
		var updatedAt string
		if err := rows.Scan(&r.ItemID, &r.ItemName, &r.StoreID, &r.StoreName,
			&r.PriceCents, &r.PurchaseUnit, &r.AmountPerPackage, &r.Preferred, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach each item's recent history once (deduped by item id).
	histByItem := make(map[int64][]PriceHistoryEntry)
	for _, r := range out {
		if _, ok := histByItem[r.ItemID]; ok {
			continue
		}
		h, err := s.ListPriceHistoryForItem(ctx, r.ItemID)
		if err != nil {
			continue
		}
		histByItem[r.ItemID] = make([]PriceHistoryEntry, len(h))
		for i, e := range h {
			histByItem[r.ItemID][i] = *e
		}
	}
	for _, r := range out {
		r.History = histByItem[r.ItemID]
	}
	return out, nil
}
