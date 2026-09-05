package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration. Values are loaded from .env (if
// present) then from environment variables. SESSION_SECRET, DATABASE_URL, and
// LISTEN_ADDR are not runtime-editable (§11.4).
type Config struct {
	AppName         string
	ListenAddr      string
	PublicBaseURL   string
	DatabaseURL     string
	SessionSecret   string
	SessionTTLHours int // default 168 (7 days)
}

// Load reads configuration from .env then the environment, applies defaults,
// and validates required values.
func Load() (*Config, error) {
	// .env is optional; ignore "file not found".
	_ = godotenv.Load()

	sessionTTL, _ := strconv.Atoi(os.Getenv("SESSION_TTL_HOURS"))
	if sessionTTL <= 0 {
		sessionTTL = 168
	}

	cfg := &Config{
		AppName:         getenv("APP_NAME", "Go Eat"),
		ListenAddr:      getenv("LISTEN_ADDR", ":8080"),
		PublicBaseURL:   getenv("PUBLIC_BASE_URL", ""),
		DatabaseURL:     getenv("DATABASE_URL", "file:./data/goeat.db"),
		SessionSecret:   os.Getenv("SESSION_SECRET"),
		SessionTTLHours: sessionTTL,
	}

	if cfg.SessionSecret == "" {
		// Dev fallback: ephemeral secret with a loud warning. Set SESSION_SECRET
		// in .env for any persistent deployment.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("SESSION_SECRET not set and could not generate one: %w", err)
		}
		cfg.SessionSecret = hex.EncodeToString(b)
		log.Println("WARNING: SESSION_SECRET not set — using an ephemeral secret. Sessions will not survive restarts. Set SESSION_SECRET in .env.")
	}

	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
