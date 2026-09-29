package db

import (
	"context"
	"time"
)

// preferred is derived from items.preferred_store_id (00033), not read from
// item_store_packages.preferred, which nothing writes any more.
const itemPackageColumns = `id, item_id, store_id, purchase_unit, amount_per_package,
	price_cents, updated_by, updated_at,
	COALESCE((SELECT i.preferred_store_id FROM items i WHERE i.id = item_store_packages.item_id) = store_id, 0)`

// UpsertItemStorePackage inserts or replaces how one item is sold at one store.
// Every write is also appended to price_history, so callers never need to
// remember to record history themselves (§ pencil-icon price editor).
func (s *store) UpsertItemStorePackage(ctx context.Context, p UpsertItemStorePackageParams) error {
	if p.PurchaseUnit == "" {
		p.PurchaseUnit = "each"
	}
	if p.AmountPerPackage == 0 {
		p.AmountPerPackage = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO item_store_packages
			(item_id, store_id, purchase_unit, amount_per_package, price_cents, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(item_id, store_id) DO UPDATE SET
			purchase_unit     = excluded.purchase_unit,
			amount_per_package = excluded.amount_per_package,
			price_cents       = excluded.price_cents,
			updated_by        = excluded.updated_by,
			updated_at        = excluded.updated_at`,
		p.ItemID, p.StoreID, p.PurchaseUnit, p.AmountPerPackage, p.PriceCents, p.UpdatedBy)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO price_history
			(item_id, store_id, price_cents, purchase_unit, amount_per_package, recorded_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.ItemID, p.StoreID, p.PriceCents, p.PurchaseUnit, p.AmountPerPackage, p.UpdatedBy)
	return err
}

// ListPriceHistory returns price_history rows for one (item, store) pair,
// newest first, capped to the most recent 20 entries.
func (s *store) ListPriceHistory(ctx context.Context, itemID, storeID int64) ([]*PriceHistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, store_id, price_cents, purchase_unit, amount_per_package, recorded_by, recorded_at
		FROM price_history
		WHERE item_id = ? AND store_id = ?
		ORDER BY recorded_at DESC
		LIMIT 20`, itemID, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PriceHistoryEntry
	for rows.Next() {
		var e PriceHistoryEntry
		var recordedAt string
		if err := rows.Scan(&e.ID, &e.ItemID, &e.StoreID, &e.PriceCents,
			&e.PurchaseUnit, &e.AmountPerPackage, &e.RecordedBy, &recordedAt); err != nil {
			return nil, err
		}
		e.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *store) ListPackagesForItem(ctx context.Context, itemID int64) ([]*ItemStorePackage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemPackageColumns+` FROM item_store_packages WHERE item_id = ? ORDER BY store_id`,
		itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItemPackageRows(rows)
}

func (s *store) ListPackagesForStore(ctx context.Context, storeID int64) ([]*ItemStorePackage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemPackageColumns+` FROM item_store_packages WHERE store_id = ? ORDER BY item_id`,
		storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItemPackageRows(rows)
}

// GetItemStorePackage returns the package row for one (item, store) pair, or
// nil, nil when none is defined.
func (s *store) GetItemStorePackage(ctx context.Context, itemID, storeID int64) (*ItemStorePackage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemPackageColumns+` FROM item_store_packages WHERE item_id = ? AND store_id = ?`,
		itemID, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanItemPackageRows(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

// DeleteItemStorePackage deletes one of itemID's store packages; a package
// id belonging to another item deletes nothing and reports ErrNotFound.
func (s *store) DeleteItemStorePackage(ctx context.Context, itemID, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM item_store_packages WHERE id = ? AND item_id = ?`, id, itemID)
	if err != nil {
		return err
	}
	return oneRow(res)
}

// SetItemStorePreferred marks (or unmarks) a store as the one the household
// buys an item from - "we only buy this here". It is a column on the item
// (items.preferred_store_id), so there is only ever one store per item and a
// second preference replaces the first; and it no longer needs a package row
// at that store, since the store alone is enough for pricing to know where
// to look. Unmarking only clears it when storeID is still the preferred one.
func (s *store) SetItemStorePreferred(ctx context.Context, itemID, storeID int64, preferred bool) error {
	if preferred {
		_, err := s.db.ExecContext(ctx,
			`UPDATE items SET preferred_store_id = ? WHERE id = ?`, storeID, itemID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET preferred_store_id = NULL WHERE id = ? AND preferred_store_id = ?`,
		itemID, storeID)
	return err
}

// SetStoreItems makes itemIDs exactly the set of household items bought only
// at storeID: each listed item moves here (from whichever store it was tied
// to before), and any item tied here that is not listed is released to "any
// store". The household_id guard drops item ids from another household
// rather than trusting them.
func (s *store) SetStoreItems(ctx context.Context, householdID, storeID int64, itemIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE items SET preferred_store_id = NULL WHERE household_id = ? AND preferred_store_id = ?`,
		householdID, storeID); err != nil {
		return err
	}
	for _, id := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE items SET preferred_store_id = ? WHERE id = ? AND household_id = ?`,
			storeID, id, householdID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanItemPackageRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*ItemStorePackage, error) {
	var out []*ItemStorePackage
	for rows.Next() {
		var p ItemStorePackage
		var updatedAt string
		var preferred int
		if err := rows.Scan(&p.ID, &p.ItemID, &p.StoreID, &p.PurchaseUnit,
			&p.AmountPerPackage, &p.PriceCents, &p.UpdatedBy, &updatedAt, &preferred); err != nil {
			return nil, err
		}
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		p.Preferred = preferred != 0
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ListPriceHistoryForItem returns every recorded price for one item across all
// stores, oldest first, capped to the most recent 200 entries.
//
// Oldest-first because a chart is drawn left to right in time, and the cap is
// applied to the *newest* end - a subquery rather than a plain LIMIT, which
// would keep the oldest 200 and draw a chart that stops months ago.
func (s *store) ListPriceHistoryForItem(ctx context.Context, itemID int64) ([]*PriceHistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, store_id, price_cents, purchase_unit, amount_per_package, recorded_by, recorded_at
		FROM (
			SELECT id, item_id, store_id, price_cents, purchase_unit, amount_per_package, recorded_by, recorded_at
			FROM price_history
			WHERE item_id = ?
			ORDER BY recorded_at DESC
			LIMIT 200
		)
		ORDER BY recorded_at ASC`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PriceHistoryEntry
	for rows.Next() {
		var e PriceHistoryEntry
		var recordedAt string
		if err := rows.Scan(&e.ID, &e.ItemID, &e.StoreID, &e.PriceCents,
			&e.PurchaseUnit, &e.AmountPerPackage, &e.RecordedBy, &recordedAt); err != nil {
			return nil, err
		}
		e.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		out = append(out, &e)
	}
	return out, rows.Err()
}
