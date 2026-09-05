package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *store) UpsertPriceCache(ctx context.Context, p UpsertPriceCacheParams) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO price_cache
			(store_id, normalized_term, price_cents, purchase_unit, pack_size, source, confidence, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(store_id, normalized_term) DO UPDATE SET
			price_cents   = excluded.price_cents,
			purchase_unit = excluded.purchase_unit,
			pack_size     = excluded.pack_size,
			source        = excluded.source,
			confidence    = excluded.confidence,
			fetched_at    = excluded.fetched_at`,
		p.StoreID, p.NormalizedTerm, p.PriceCents, p.PurchaseUnit, p.PackSize, p.Source, p.Confidence,
	)
	return err
}

func (s *store) GetPriceCache(ctx context.Context, storeID int64, normalizedTerm string) (*PriceCache, error) {
	var pc PriceCache
	var fetchedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, store_id, normalized_term, price_cents, purchase_unit, pack_size,
		       source, confidence, fetched_at
		FROM price_cache
		WHERE store_id = ? AND normalized_term = ?`,
		storeID, normalizedTerm,
	).Scan(&pc.ID, &pc.StoreID, &pc.NormalizedTerm, &pc.PriceCents,
		&pc.PurchaseUnit, &pc.PackSize, &pc.Source, &pc.Confidence, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pc.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)
	return &pc, nil
}
