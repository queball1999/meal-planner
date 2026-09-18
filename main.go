package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"goeat/catalog"
	"goeat/config"
	"goeat/cryptbox"
	"goeat/db"
	"goeat/llm"
	"goeat/settings"
	"goeat/web"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	// Seed the settings table from .env on first boot, then apply any
	// admin-edited values from a past Settings-page save on top of cfg -
	// same one-time, before-subsystems-are-built timing .env itself gets.
	seedCtx := context.Background()
	if err := settings.Seed(seedCtx, store, cfg); err != nil {
		log.Fatalf("settings: seed: %v", err)
	}
	if err := settings.Apply(seedCtx, store, cfg, log.Printf); err != nil {
		log.Fatalf("settings: apply: %v", err)
	}

	// Items catalog: seed global unit conversions, then (if setup is done) seed
	// the household's starter catalog and link any pre-existing ingredient /
	// pantry rows to catalog items (00010_items.sql backfill).
	if err := catalog.SeedGlobalConversions(seedCtx, store); err != nil {
		log.Printf("catalog: seed global conversions: %v", err)
	}
	if hh, err := store.GetHousehold(seedCtx); err != nil {
		log.Printf("catalog: household lookup: %v", err)
	} else if hh != nil {
		if err := catalog.SeedHousehold(seedCtx, store, hh.ID); err != nil {
			log.Printf("catalog: seed household: %v", err)
		}
		if err := catalog.BackfillHousehold(seedCtx, store, hh.ID); err != nil {
			log.Printf("catalog: backfill household: %v", err)
		}
	}

	var gen llm.Generator
	if cfg.Provider != "" {
		g, err := llm.NewGenerator(cfg)
		if err != nil {
			log.Printf("llm: skipping generator: %v", err)
		} else {
			gen = g
			log.Printf("llm: provider=%s model=%s", gen.ProviderName(), gen.ModelName())
		}
	}

	box := cryptbox.New(cfg.SessionSecret)
	srv := web.NewServer(cfg, store, gen, version, box)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Home Assistant shopping-list pull loop; no-ops until HA is configured
	// with a non-zero interval.
	go srv.RunHAScheduler(ctx)

	// Auto-plan generation the night before the week starts; no-ops until
	// AUTO_PLAN_HOUR is set (Settings → Calendar).
	go srv.RunAutoPlanScheduler(ctx)

	// Slow, background backfill of catalog-item photos (freefoodphotos.com,
	// routed through the same FlareSolverr/Browserless-escalating fetch as
	// store scraping); no-ops until ITEM_IMAGE_DIR is set.
	go srv.RunImageBackfillScheduler(ctx)

	log.Printf("go-eat %s listening on %s", version, cfg.ListenAddr)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
}
