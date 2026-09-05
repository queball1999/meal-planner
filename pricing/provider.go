// Package pricing implements the price-resolution chain (§6.1–§6.6).
// All five PriceProvider adapters satisfy the same interface; no provider
// writes SQL directly — they return a PriceResult and the chain's caller
// persists it to price_cache via db.Store.
package pricing

import (
	"context"
	"time"
)

// Confidence levels, ordered from most to least authoritative (§6.1, §6.6).
const (
	ConfidenceLive     = "live"
	ConfidenceCached   = "cached"
	ConfidenceManual   = "manual"
	ConfidenceScrape   = "scrape"
	ConfidenceEstimate = "estimate"
)

// confidenceRank maps each level to an integer for comparison.
var confidenceRank = map[string]int{
	ConfidenceLive:     5,
	ConfidenceCached:   4,
	ConfidenceManual:   3,
	ConfidenceScrape:   2,
	ConfidenceEstimate: 1,
}

// ConfidenceAtLeast returns true when got is at least as authoritative as want.
func ConfidenceAtLeast(got, want string) bool {
	return confidenceRank[got] >= confidenceRank[want]
}

// PriceResult is the output of a successful provider lookup (§6.2).
type PriceResult struct {
	PriceCents   int64
	PurchaseUnit string  // e.g. "each", "oz", "lb"
	PackSize     float64 // purchasable unit size (e.g. 5 for a 5-lb bag)
	Source       string  // raw source tag stored in price_cache
	Confidence   string  // one of the Confidence* constants
	FetchedAt    time.Time
}

// PriceProvider is the interface every pricing adapter implements (§6.2).
// Lookup returns (nil, nil) to signal "no answer" — the chain tries the next
// provider. A non-nil error is a hard failure (network down, DB error).
type PriceProvider interface {
	Lookup(ctx context.Context, term string, storeID int64, region string) (*PriceResult, error)
	Name() string
}
