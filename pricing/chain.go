package pricing

import (
	"context"
	"fmt"
	"log"
)

// Chain resolves a price by walking an ordered list of PriceProviders and
// stopping at the first result whose confidence meets the minimum threshold
// (§6.1). The caller is responsible for persisting results to price_cache.
type Chain struct {
	providers     []PriceProvider
	minConfidence string                                                   // stop when a result is at least this confident
	persistFn     func(context.Context, *PriceResult, int64, string) error // optional write-back
}

// ChainOption configures a Chain.
type ChainOption func(*Chain)

// WithMinConfidence sets the minimum confidence level to accept. Defaults to
// ConfidenceEstimate (accept anything).
func WithMinConfidence(c string) ChainOption {
	return func(ch *Chain) { ch.minConfidence = c }
}

// WithPersist sets a write-back function called after a live result is obtained.
// Used to cache live/scrape/estimate results in price_cache (§6.5).
func WithPersist(fn func(ctx context.Context, r *PriceResult, storeID int64, term string) error) ChainOption {
	return func(ch *Chain) { ch.persistFn = fn }
}

// NewChain constructs a resolution chain from an ordered provider list.
func NewChain(providers []PriceProvider, opts ...ChainOption) *Chain {
	ch := &Chain{
		providers:     providers,
		minConfidence: ConfidenceEstimate,
	}
	for _, o := range opts {
		o(ch)
	}
	return ch
}

// Resolve walks providers in order and returns the first result at or above the
// configured minimum confidence. Returns nil when all providers answer "no answer".
func (ch *Chain) Resolve(ctx context.Context, term string, storeID int64, region string) (*PriceResult, error) {
	normalized := Normalize(term)
	for _, p := range ch.providers {
		r, err := p.Lookup(ctx, normalized, storeID, region)
		if err != nil {
			log.Printf("pricing: provider %s error for %q: %v", p.Name(), normalized, err)
			continue // fail soft - try next provider
		}
		if r == nil {
			continue // "no answer"
		}
		if !ConfidenceAtLeast(r.Confidence, ch.minConfidence) {
			continue // below threshold - keep looking
		}
		// Write-back for non-cache providers so future cache hits are available.
		if ch.persistFn != nil && r.Source != "cache" {
			if werr := ch.persistFn(ctx, r, storeID, normalized); werr != nil {
				log.Printf("pricing: persist write-back error: %v", werr)
			}
		}
		return r, nil
	}
	return nil, fmt.Errorf("pricing: no price found for %q at store %d", normalized, storeID)
}
