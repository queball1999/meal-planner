package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *store) UpsertItemProductMap(ctx context.Context, p UpsertItemProductMapParams) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO item_product_map
			(store_id, normalized_term, chosen_product, pack_size, purchase_unit, barcode, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(store_id, normalized_term) DO UPDATE SET
			chosen_product = excluded.chosen_product,
			pack_size      = excluded.pack_size,
			purchase_unit  = excluded.purchase_unit,
			barcode        = excluded.barcode,
			updated_at     = excluded.updated_at`,
		p.StoreID, p.NormalizedTerm, p.ChosenProduct, p.PackSize, p.PurchaseUnit, p.Barcode,
	)
	return err
}

func (s *store) GetItemProductMap(ctx context.Context, storeID int64, normalizedTerm string) (*ItemProductMap, error) {
	var m ItemProductMap
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, store_id, normalized_term, chosen_product, pack_size, purchase_unit, barcode, updated_at
		FROM item_product_map
		WHERE store_id = ? AND normalized_term = ?`,
		storeID, normalizedTerm,
	).Scan(&m.ID, &m.StoreID, &m.NormalizedTerm, &m.ChosenProduct,
		&m.PackSize, &m.PurchaseUnit, &m.Barcode, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &m, nil
}
