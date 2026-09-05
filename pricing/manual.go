package pricing

import (
	"context"
	"time"

	"goeat/db"
)

// ManualProvider reads operator-entered prices from manual_prices (§6.2).
// Region is the household's ZIP/metro, matched exactly.
type ManualProvider struct {
	store  db.Store
	region string // household ZIP passed once at construction
}

func NewManualProvider(store db.Store, region string) *ManualProvider {
	return &ManualProvider{store: store, region: region}
}

func (m *ManualProvider) Name() string { return "manual" }

func (m *ManualProvider) Lookup(ctx context.Context, term string, storeID int64, region string) (*PriceResult, error) {
	r := region
	if r == "" {
		r = m.region
	}
	mp, err := m.store.GetManualPrice(ctx, storeID, r, term)
	if err != nil || mp == nil {
		return nil, err
	}
	return &PriceResult{
		PriceCents:   mp.PriceCents,
		PurchaseUnit: mp.PurchaseUnit,
		PackSize:     mp.PackSize,
		Source:       "manual",
		Confidence:   ConfidenceManual,
		FetchedAt:    mp.UpdatedAt,
	}, nil
}

// ListAllForStore returns all manual prices for admin display.
func (m *ManualProvider) ListAllForStore(ctx context.Context, storeID int64) ([]*db.ManualPrice, error) {
	return m.store.ListManualPrices(ctx, storeID)
}

// Upsert writes an operator price entry.
func (m *ManualProvider) Upsert(ctx context.Context, p db.UpsertManualPriceParams) error {
	if p.Region == "" {
		p.Region = m.region
	}
	return m.store.UpsertManualPrice(ctx, p)
}

// Delete removes one manual price row.
func (m *ManualProvider) Delete(ctx context.Context, id int64) error {
	return m.store.DeleteManualPrice(ctx, id)
}

// nowISO returns the current UTC time in RFC3339Nano format (unused here but
// kept as a package-level helper for other providers that build timestamps).
func nowISO() string { return time.Now().UTC().Format(time.RFC3339Nano) }
