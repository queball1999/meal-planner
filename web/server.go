package web

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/csrf"

	"goeat/agent"
	"goeat/config"
	"goeat/cryptbox"
	"goeat/db"
	"goeat/homeassistant"
	"goeat/llm"
	"goeat/middleware"
	"goeat/plan"
	"goeat/pricing"
	"goeat/scrape"
	"goeat/settings"
)

// Server holds shared dependencies and the fully-wired HTTP handler.
type Server struct {
	cfg          *config.Config
	store        db.Store
	gen          llm.Generator // nil when no LLM is configured
	chain        *pricing.Chain
	jobs         *plan.JobManager
	haScheduler  *homeassistant.Scheduler
	version      string
	imageDir     string        // writable dir for recipe images (§5.7); "" = skip download
	itemImageDir string        // writable dir for catalog-item images (00010); "" = skip download
	box          *cryptbox.Box // seals/opens secrets at rest (HA token)
	handler      http.Handler

	startedAt time.Time // process start, for the About page's uptime tile

	autoPlanMu        sync.Mutex
	autoPlanCheckedAt time.Time // last RunAutoPlanScheduler tick, whether or not it fired

	// repriceMu guards repricing, keyed by plan id. Rebuilding a shopping list
	// can take minutes (a price lookup per ingredient), and a user nudging
	// several days' headcounts in a row would otherwise start overlapping
	// rebuilds that race each other's DELETE-then-INSERT.
	repriceMu      sync.Mutex
	repricingPlans map[int64]bool

	// pendingChat holds the assistant's paused runs, keyed by household - a
	// mutating tool call waiting on a yes.
	//
	// In memory rather than in the database: a pending call is meaningful for
	// the seconds between the assistant proposing it and a person answering,
	// and a restart in that window should forget it. Persisting it would mean
	// an "apply?" prompt surviving a reboot and applying a change nobody
	// remembers being asked about.
	pendingMu   sync.Mutex
	pendingChat map[int64]*agent.Pending
}

// NewServer wires up routes, session loading, and CSRF middleware, then
// returns a ready-to-run Server.
func NewServer(cfg *config.Config, store db.Store, gen llm.Generator, version string, box *cryptbox.Box) *Server {
	chain := buildChain(cfg, store, gen)
	if gen != nil {
		gen = llm.NewDebugLogger(gen) // captures every call into llm.GlobalDebugLog
	}
	s := &Server{
		cfg:            cfg,
		store:          store,
		gen:            gen,
		chain:          chain,
		jobs:           plan.NewJobManager(),
		haScheduler:    homeassistant.NewScheduler(store, cfg, box),
		version:        version,
		imageDir:       cfg.RecipeImageDir,
		itemImageDir:   cfg.ItemImageDir,
		box:            box,
		startedAt:      time.Now(),
		repricingPlans: make(map[int64]bool),
		pendingChat:    make(map[int64]*agent.Pending),
	}
	s.handler = s.buildHandler()
	return s
}

// RunHAScheduler runs the Home Assistant shopping-list pull loop until ctx is
// cancelled; no-ops until HA is configured with a non-zero interval. Owned by
// Server (rather than constructed ad hoc in main.go) so the About page can
// read its last-run state through the same instance that's actually ticking.
func (s *Server) RunHAScheduler(ctx context.Context) {
	s.haScheduler.Run(ctx)
}

// buildChain constructs the five-provider resolution chain in §6.1 order:
// Kroger (official API) → cache → manual → scraper → AI estimate.
func buildChain(cfg *config.Config, store db.Store, gen llm.Generator) *pricing.Chain {
	region := "" // resolved per-request from household.ZIPCode; empty here is fine

	var providers []pricing.PriceProvider

	if k := pricing.NewKrogerProvider(cfg.KrogerClientID, cfg.KrogerClientSecret, cfg.KrogerLocationID); k != nil {
		providers = append(providers, k)
	}
	providers = append(providers, pricing.NewCacheProvider(store, cfg.PriceCacheTTLHours))
	providers = append(providers, pricing.NewManualProvider(store, region))
	providers = append(providers, pricing.NewScraperProvider(store, func(ctx context.Context) scrape.RenderConfig {
		return settings.LiveRenderConfig(ctx, store, cfg)
	}, gen))
	if gen != nil {
		providers = append(providers, pricing.NewAIEstimateProvider(gen, region))
	}

	persistFn := func(ctx context.Context, r *pricing.PriceResult, storeID int64, term string) error {
		return store.UpsertPriceCache(ctx, db.UpsertPriceCacheParams{
			StoreID:        storeID,
			NormalizedTerm: term,
			PriceCents:     r.PriceCents,
			PurchaseUnit:   r.PurchaseUnit,
			PackSize:       r.PackSize,
			Source:         r.Source,
			Confidence:     r.Confidence,
		})
	}

	return pricing.NewChain(providers, pricing.WithPersist(persistFn))
}

// buildHandler composes the middleware stack around the route mux:
//
//	CSRF → LoadSession → mux
//
// gorilla/csrf only enforces on non-safe methods (POST/PUT/PATCH/DELETE),
// so wrapping the whole mux is safe for GET/HEAD.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	var h http.Handler = mux
	h = middleware.LoadSession(s.store)(h)

	csrfOpts := []csrf.Option{
		csrf.Secure(false),                  // Allow plain HTTP on LAN (§9.3)
		csrf.SameSite(csrf.SameSiteLaxMode), // Required alongside Secure(false)
		csrf.HttpOnly(true),
		csrf.ErrorHandler(http.HandlerFunc(s.handleCSRFError)),
	}
	// Trust the external origin declared in PUBLIC_BASE_URL. This is required
	// when Docker maps a different host port (e.g. 8081:8080) so the browser's
	// Origin header doesn't match the server's internal bind address.
	if s.cfg.PublicBaseURL != "" {
		if u, err := url.Parse(s.cfg.PublicBaseURL); err == nil && u.Host != "" {
			csrfOpts = append(csrfOpts, csrf.TrustedOrigins([]string{u.Host}))
		}
	}
	h = csrf.Protect([]byte(s.cfg.SessionSecret), csrfOpts...)(h)
	h = middleware.Timing(h) // outermost: footer's "Page" time includes CSRF + session overhead

	return h
}

// Run starts the HTTP server and blocks until ctx is cancelled. On
// cancellation it performs a graceful shutdown (10 s deadline).
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      s.handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
