package pricing

import (
	"context"
	"time"

	"goeat/db"
)

// CacheProvider reads price_cache for a fresh hit (§6.2, §6.5).
// It carries forward the original source's confidence - a cached AI estimate
// is never promoted to "live" just because it came from the DB.
type CacheProvider struct {
	store db.Store
	ttl   time.Duration // freshness window; zero means no expiry check
}

// NewCacheProvider constructs a CacheProvider with the given TTL.
// ttlHours=0 disables expiry (accept any cached entry).
func NewCacheProvider(store db.Store, ttlHours int) *CacheProvider {
	var ttl time.Duration
	if ttlHours > 0 {
		ttl = time.Duration(ttlHours) * time.Hour
	}
	return &CacheProvider{store: store, ttl: ttl}
}

func (c *CacheProvider) Name() string { return "cache" }

func (c *CacheProvider) Lookup(ctx context.Context, term string, storeID int64, _ string) (*PriceResult, error) {
	pc, err := c.store.GetPriceCache(ctx, storeID, term)
	if err != nil || pc == nil {
		return nil, err
	}
	// Reject stale entries - fall through to next provider.
	if c.ttl > 0 && time.Since(pc.FetchedAt) > c.ttl {
		return nil, nil
	}
	// Heal the "dozen" + 12 shape that older estimate rows were written with
	// before sanitizeEstimatePack existed (a carton is 1 dozen, not 12).
	unit, packSize := sanitizeEstimatePack(pc.PurchaseUnit, pc.PackSize)
	return &PriceResult{
		PriceCents:   pc.PriceCents,
		PurchaseUnit: unit,
		PackSize:     packSize,
		Source:       "cache",
		Confidence:   pc.Confidence, // original confidence, not promoted
		FetchedAt:    pc.FetchedAt,
	}, nil
}
