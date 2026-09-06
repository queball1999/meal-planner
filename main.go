package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"goeat/config"
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

	srv := web.NewServer(cfg, store, gen, version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("go-eat %s listening on %s", version, cfg.ListenAddr)
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
}
