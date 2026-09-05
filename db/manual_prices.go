package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *store) UpsertManualPrice(ctx context.Context, p UpsertManualPriceParams) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO manual_prices
			(store_id, region, normalized_term, price_cents, pack_size, purchase_unit, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(store_id, region, normalized_term) DO UPDATE SET
			price_cents   = excluded.price_cents,
			pack_size     = excluded.pack_size,
			purchase_unit = excluded.purchase_unit,
			updated_by    = excluded.updated_by,
			updated_at    = excluded.updated_at`,
		p.StoreID, p.Region, p.NormalizedTerm, p.PriceCents, p.PackSize, p.PurchaseUnit, p.UpdatedBy,
	)
	return err
}

func (s *store) GetManualPrice(ctx context.Context, storeID int64, region, normalizedTerm string) (*ManualPrice, error) {
	var mp ManualPrice
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, store_id, region, normalized_term, price_cents, pack_size, purchase_unit, updated_by, updated_at
		FROM manual_prices
		WHERE store_id = ? AND region = ? AND normalized_term = ?`,
		storeID, region, normalizedTerm,
	).Scan(&mp.ID, &mp.StoreID, &mp.Region, &mp.NormalizedTerm,
		&mp.PriceCents, &mp.PackSize, &mp.PurchaseUnit, &mp.UpdatedBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mp.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &mp, nil
}

func (s *store) ListManualPrices(ctx context.Context, storeID int64) ([]*ManualPrice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, store_id, region, normalized_term, price_cents, pack_size, purchase_unit, updated_by, updated_at
		FROM manual_prices WHERE store_id = ?
		ORDER BY normalized_term`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ManualPrice
	for rows.Next() {
		var mp ManualPrice
		var updatedAt string
		if err := rows.Scan(&mp.ID, &mp.StoreID, &mp.Region, &mp.NormalizedTerm,
			&mp.PriceCents, &mp.PackSize, &mp.PurchaseUnit, &mp.UpdatedBy, &updatedAt); err != nil {
			return nil, err
		}
		mp.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &mp)
	}
	return out, rows.Err()
}

func (s *store) DeleteManualPrice(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM manual_prices WHERE id = ?`, id)
	return err
}
