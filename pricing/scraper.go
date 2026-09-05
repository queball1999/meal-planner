package pricing

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"goeat/db"
	"goeat/scrape"
)

const (
	scrapeRateLimit = 2 * time.Second // minimum interval between requests per store
	scrapeTimeout   = 10 * time.Second
)

// ScraperProvider prices items using per-store scrape configs (§6.2, §6.7).
// Each store's scraper is independently rate-limited. Fails soft: any error
// returns nil ("no answer"), never breaks the resolution chain.
type ScraperProvider struct {
	dbStore        db.Store
	flareSolverrURL string

	mu      sync.Mutex
	lastReq map[int64]time.Time // per-store rate limiter
}

func NewScraperProvider(dbStore db.Store, flareSolverrURL string) *ScraperProvider {
	return &ScraperProvider{
		dbStore:         dbStore,
		flareSolverrURL: flareSolverrURL,
		lastReq:         make(map[int64]time.Time),
	}
}

func (sp *ScraperProvider) Name() string { return "scraper" }

func (sp *ScraperProvider) Lookup(ctx context.Context, term string, storeID int64, _ string) (*PriceResult, error) {
	cfg, err := sp.dbStore.GetScrapeConfigByStore(ctx, storeID)
	if err != nil || cfg == nil || cfg.Status == "unconfigured" {
		return nil, nil // not configured for this store
	}
	if cfg.SearchURLTemplate == "" {
		return nil, nil
	}

	// Rate-limit per store.
	sp.mu.Lock()
	last := sp.lastReq[storeID]
	if time.Since(last) < scrapeRateLimit {
		sp.mu.Unlock()
		return nil, nil // too soon — skip rather than block
	}
	sp.lastReq[storeID] = time.Now()
	sp.mu.Unlock()

	searchURL := strings.ReplaceAll(cfg.SearchURLTemplate, "{term}", url.QueryEscape(term))

	ctx, cancel := context.WithTimeout(ctx, scrapeTimeout)
	defer cancel()

	var result *scrape.FetchResult
	result, err = scrape.Fetch(ctx, searchURL)
	if err != nil && sp.flareSolverrURL != "" {
		// Retry via FlareSolverr when direct fetch fails.
		result, err = scrape.FetchViaProxy(ctx, searchURL, sp.flareSolverrURL)
	}
	if err != nil {
		log.Printf("scraper: fetch error store=%d: %v", storeID, err)
		// Mark degraded if we see consistent failures (handled externally via Test button).
		return nil, nil
	}

	sels, _ := scrape.ParseSelectors(cfg.SelectorsJSON)
	products := scrape.Extract(result.HTML, sels, 5)
	if len(products) == 0 {
		return nil, nil
	}

	best := products[0]
	if best.Price <= 0 {
		return nil, nil
	}

	// Validate: $0 or non-numeric means degraded config — log but fail soft.
	priceCents := int64(best.Price * 100)
	if priceCents <= 0 {
		log.Printf("scraper: implausible price %.2f for %q store=%d", best.Price, term, storeID)
		return nil, nil
	}

	packSize, unit := parseSizeStr(best.PackSize)
	return &PriceResult{
		PriceCents:   priceCents,
		PurchaseUnit: unit,
		PackSize:     packSize,
		Source:       "scrape",
		Confidence:   ConfidenceScrape,
		FetchedAt:    time.Now().UTC(),
	}, nil
}

func parseSizeStr(s string) (float64, string) {
	if s == "" {
		return 1, "each"
	}
	var size float64
	var unit string
	_, err := fmt.Sscanf(strings.ToLower(strings.TrimSpace(s)), "%f %s", &size, &unit)
	if err != nil || size <= 0 {
		return 1, "each"
	}
	return size, unit
}
