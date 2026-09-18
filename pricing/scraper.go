package pricing

import (
	"context"
	"encoding/json"
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
	// blockedLogged dedupes the "mark blocked + audit log" transition under
	// concurrent lookups against the same freshly-blocked store (the parallel
	// per-store fan-out in pricing.ResolvePricing can call Lookup for the same
	// store from more than one item's goroutine before the first write lands).
	// Either write alone is correct - this only avoids duplicate log rows.
	blockedLogged map[int64]bool
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
		dbStore:       dbStore,
		render:        render,
		gen:           gen,
		lastReq:       make(map[int64]time.Time),
		blockedLogged: make(map[int64]bool),
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

	render := sp.render(ctx)

	// An admin-solved clearance (live CDP session or pasted cookies) beats
	// running the whole automated chain again: one Browserless render call,
	// no FlareSolverr round trip. Falls through to the ordinary chain below
	// on any miss, and drops the clearance so a stale one doesn't slow down
	// every future lookup for this store.
	var result *scrape.FetchResult
	if cleared, ok := sp.tryClearance(ctx, storeID, searchURL, render); ok {
		result = cleared
	}

	if result == nil {
		// FetchSmart sends browser headers with a cookie jar and escalates to
		// the configured headless browser when the store answers with a bot
		// wall or a JavaScript shell. A store context (a selected store, kept
		// in a cookie) switches it to a scripted session - some retailers show
		// no prices at all until one is chosen.
		smart, err := scrape.FetchSmartWithContext(ctx, searchURL, render,
			scrape.ParseStoreContext(cfg.ContextJSON))
		if smart != nil {
			result = smart.FetchResult
			if smart.Challenge != "" {
				log.Printf("scraper: store=%d %s", storeID, smart.Challenge)
				// The full automated chain ran (both renderers, or whichever
				// was configured) and still hit a genuine bot-wall signature -
				// not just an empty JS shell or no products, either of which a
				// human solving a CAPTCHA would do nothing about. Flag it once
				// per block so an admin can solve it from the Scrape Config
				// page; this run still fails soft below exactly as before.
				if smart.ViaProxy && render.Enabled() && scrape.IsBotWall(smart.Challenge) {
					sp.markBlocked(ctx, cfg, smart.Challenge, searchURL)
				}
			}
		}
		if err != nil || result == nil {
			log.Printf("scraper: fetch error store=%d: %v", storeID, err)
			sp.markStatus(ctx, cfg, "degraded")
			return nil, nil
		}
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

	// An admin may have picked the right product from among several on the
	// search page (the scrape-config product picker, §6.7) - prefer it by
	// name over whichever priced result happens to come first on the page.
	var pinnedName string
	if pinned, perr := sp.dbStore.GetItemProductMap(ctx, storeID, term); perr == nil && pinned != nil {
		pinnedName = pinned.ChosenProduct
	}

	best := bestPriced(products, pinnedName)
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
	if cfg.Blocked() {
		// The site let a lookup through - via a saved clearance, or the
		// automated chain succeeding on its own again - so whatever human
		// solved it (or the wall lifting itself) fixed it; nothing left for
		// an admin to do on the Scrape Config page.
		if err := sp.dbStore.ClearScrapeConfigBlocked(ctx, storeID); err != nil {
			log.Printf("scraper: clear blocked store=%d: %v", storeID, err)
		}
		sp.mu.Lock()
		delete(sp.blockedLogged, storeID)
		sp.mu.Unlock()
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

// bestPriced returns the pinned product - the one an admin picked out of
// several on the search page, by exact name - if it's still there and priced,
// else the first product carrying a usable price at all.
func bestPriced(products []scrape.ExtractedProduct, pinnedName string) *scrape.ExtractedProduct {
	if pinnedName != "" {
		for i := range products {
			if products[i].Price > 0 && strings.EqualFold(strings.TrimSpace(products[i].Name), pinnedName) {
				return &products[i]
			}
		}
	}
	for i := range products {
		if products[i].Price > 0 {
			return &products[i]
		}
	}
	return nil
}

// tryClearance attempts searchURL through a saved admin-solved clearance
// (scrape_clearances), if one exists, is unexpired, and a Browserless
// renderer is configured. Reports ok only on a real, unchallenged page - any
// miss (no clearance, expired, or the site still challenges it) deletes a
// stale row and returns false so the caller falls through to the ordinary
// FetchSmart chain.
func (sp *ScraperProvider) tryClearance(ctx context.Context, storeID int64, searchURL string, render scrape.RenderConfig) (*scrape.FetchResult, bool) {
	var browserless scrape.Renderer
	for _, r := range render.Renderers() {
		if r.Backend == scrape.RendererBrowserless {
			browserless = r
			break
		}
	}
	if !browserless.Enabled() {
		return nil, false
	}

	saved, err := sp.dbStore.GetScrapeClearance(ctx, storeID)
	if err != nil {
		log.Printf("scraper: load clearance store=%d: %v", storeID, err)
		return nil, false
	}
	if saved == nil {
		return nil, false
	}
	if time.Now().After(saved.ExpiresAt) {
		if derr := sp.dbStore.DeleteScrapeClearance(ctx, storeID); derr != nil {
			log.Printf("scraper: delete expired clearance store=%d: %v", storeID, derr)
		}
		return nil, false
	}

	var cookies []scrape.Cookie
	if uerr := json.Unmarshal([]byte(saved.CookiesJSON), &cookies); uerr != nil {
		log.Printf("scraper: unmarshal saved clearance store=%d: %v", storeID, uerr)
		return nil, false
	}

	result, ok := scrape.FetchWithSavedClearance(ctx, searchURL, browserless,
		&scrape.Clearance{Cookies: cookies, UserAgent: saved.UserAgent})
	if !ok {
		// Stale: the site no longer accepts it, or it expired server-side
		// before our own TTL caught up. Drop it so future lookups don't keep
		// paying for a Browserless call that's never going to work.
		if derr := sp.dbStore.DeleteScrapeClearance(ctx, storeID); derr != nil {
			log.Printf("scraper: delete stale clearance store=%d: %v", storeID, derr)
		}
		return nil, false
	}
	return result, true
}

// markBlocked flags cfg's store as waiting on a human to solve a bot wall,
// once per block - a lookup that hits this every single ingredient while a
// store stays blocked would otherwise write and log on every single call.
// blockedLogged is cleared by ClearScrapeConfigBlocked's callers implicitly
// on next process start; within a run it just needs to dedupe repeats.
func (sp *ScraperProvider) markBlocked(ctx context.Context, cfg *db.ScrapeConfig, reason, blockedURL string) {
	sp.mu.Lock()
	if sp.blockedLogged[cfg.StoreID] {
		sp.mu.Unlock()
		return
	}
	sp.blockedLogged[cfg.StoreID] = true
	sp.mu.Unlock()

	if err := sp.dbStore.MarkScrapeConfigBlocked(ctx, cfg.StoreID, reason, blockedURL); err != nil {
		log.Printf("scraper: mark blocked store=%d: %v", cfg.StoreID, err)
		return
	}
	_ = sp.dbStore.LogEvent(ctx, db.AppEvent{
		Action:     "scrape.blocked",
		TargetType: "store",
		TargetID:   fmt.Sprintf("%d", cfg.StoreID),
		Metadata:   reason,
		Status:     "error",
	})
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

// ParsePackSize parses a free-text pack size ("12 oz", "1.5 dozen") into a
// quantity and unit, defaulting to "1 each" when it can't be read. Exported
// for the scrape-config product picker (§6.7), which stores the admin's
// chosen product the same way a lookup would.
func ParsePackSize(s string) (float64, string) {
	return parseSizeStr(s)
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
