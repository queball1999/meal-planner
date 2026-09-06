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
	// A lookup that escalates to a headless browser is doing real work: two
	// renderers get 45s each, and a scripted store-context session walks
	// pre-warm pages before the search. The old 10s budget cancelled every
	// one of those before it could finish, which looked like the store had
	// simply refused us. Results are cached in price_cache, so this cost is
	// paid about once per ingredient per cache window.
	scrapeTimeout   = 90 * time.Second
	scrapeAITimeout = 180 * time.Second

	// maxPlausibleCents rejects parse errors that turn "$4.99 / 100 ct" into a
	// four-figure line item.
	maxPlausibleCents = 100_000 // $1,000
)

// ScraperProvider prices items using per-store scrape configs (§6.2, §6.7).
// Each store's scraper is independently rate-limited. Fails soft: any error
// returns nil ("no answer"), never breaks the resolution chain.
type ScraperProvider struct {
	dbStore db.Store
	// render resolves the headless-browser config at call time so a settings
	// change takes effect without a restart.
	render func(context.Context) scrape.RenderConfig
	gen    scrape.AIClient // nil when no LLM is configured

	mu      sync.Mutex
	lastReq map[int64]time.Time // per-store rate limiter
}

// NewScraperProvider builds the scrape tier. render supplies the current
// headless-browser config (may return a disabled one); gen may be nil, and
// when present it powers "auto_ai" configs, where the model reads prices off
// pages whose markup no selector can pin down.
func NewScraperProvider(dbStore db.Store, render func(context.Context) scrape.RenderConfig, gen scrape.AIClient) *ScraperProvider {
	if render == nil {
		render = func(context.Context) scrape.RenderConfig { return scrape.RenderConfig{} }
	}
	return &ScraperProvider{
		dbStore: dbStore,
		render:  render,
		gen:     gen,
		lastReq: make(map[int64]time.Time),
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
		return nil, nil // too soon - skip rather than block
	}
	sp.lastReq[storeID] = time.Now()
	sp.mu.Unlock()

	searchURL := strings.ReplaceAll(cfg.SearchURLTemplate, "{term}", url.QueryEscape(term))

	// AI reading of a page takes far longer than a fetch-and-parse.
	timeout := scrapeTimeout
	if cfg.Mode == "auto_ai" && sp.gen != nil {
		timeout = scrapeAITimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// FetchSmart sends browser headers with a cookie jar and escalates to the
	// configured headless browser when the store answers with a bot wall or a
	// JavaScript shell. A store context (a selected store, kept in a cookie)
	// switches it to a scripted session - some retailers show no prices at
	// all until one is chosen.
	smart, err := scrape.FetchSmartWithContext(ctx, searchURL, sp.render(ctx),
		scrape.ParseStoreContext(cfg.ContextJSON))
	var result *scrape.FetchResult
	if smart != nil {
		result = smart.FetchResult
		if smart.Challenge != "" {
			log.Printf("scraper: store=%d %s", storeID, smart.Challenge)
		}
	}
	if err != nil || result == nil {
		log.Printf("scraper: fetch error store=%d: %v", storeID, err)
		sp.markStatus(ctx, cfg, "degraded")
		return nil, nil
	}

	sels, _ := scrape.ParseSelectors(cfg.SelectorsJSON)
	products := scrape.Extract(result.HTML, sels, 5)

	// auto_ai: when selectors and structured data both come up empty, let the
	// model read the page. The answer is cached by the caller in price_cache,
	// so this costs about one LLM call per ingredient per cache window.
	if len(products) == 0 && cfg.Mode == "auto_ai" && sp.gen != nil {
		aiProducts, aiErr := scrape.ExtractWithAI(ctx, sp.gen, result.HTML, term)
		if aiErr != nil {
			log.Printf("scraper: ai extract store=%d: %v", storeID, aiErr)
		}
		products = aiProducts
	}

	if len(products) == 0 {
		sp.markStatus(ctx, cfg, "degraded")
		return nil, nil
	}

	best := bestPriced(products)
	if best == nil {
		sp.markStatus(ctx, cfg, "degraded")
		return nil, nil
	}

	// Validate: $0 or non-numeric means degraded config - log but fail soft.
	priceCents := int64(best.Price*100 + 0.5)
	if priceCents <= 0 || priceCents > maxPlausibleCents {
		log.Printf("scraper: implausible price %.2f for %q store=%d", best.Price, term, storeID)
		sp.markStatus(ctx, cfg, "degraded")
		return nil, nil
	}
	sp.markStatus(ctx, cfg, "active")

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

// bestPriced returns the first product carrying a usable price.
func bestPriced(products []scrape.ExtractedProduct) *scrape.ExtractedProduct {
	for i := range products {
		if products[i].Price > 0 {
			return &products[i]
		}
	}
	return nil
}

// markStatus records whether a store's scraper is still working, so the config
// page shows reality instead of the result of the last manual Test click.
// Writes only on an actual change to keep lookups read-mostly.
func (sp *ScraperProvider) markStatus(ctx context.Context, cfg *db.ScrapeConfig, status string) {
	if cfg.Status == status {
		return
	}
	err := sp.dbStore.UpdateScrapeConfig(ctx, db.UpdateScrapeConfigParams{
		ID:                cfg.ID,
		SearchURLTemplate: cfg.SearchURLTemplate,
		SelectorsJSON:     cfg.SelectorsJSON,
		Mode:              cfg.Mode,
		AIAssisted:        cfg.AIAssisted,
		Status:            status,
		LastTestedAt:      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		log.Printf("scraper: status update store=%d: %v", cfg.StoreID, err)
	}
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
