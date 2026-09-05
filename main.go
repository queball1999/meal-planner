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
