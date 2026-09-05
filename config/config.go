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

	// ── AI provider (§11.2) ───────────────────────────────────────────────
	// Provider selects the active profile: anthropic | openai | google | openai_compatible
	Provider        string
	AnthropicAPIKey string
	AnthropicModel  string // default "claude-sonnet-5"
	// OpenAI-compatible profiles (openai / google / openai_compatible)
	LLMAPIUrl    string
	LLMModel     string
	LLMAPIKey    string
	LLMMaxTokens int     // default 4096
	LLMTemp      float64 // default 0.7
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

	maxToks, _ := strconv.Atoi(os.Getenv("LLM_MAX_TOKENS"))
	if maxToks <= 0 {
		maxToks = 4096
	}
	temp, _ := strconv.ParseFloat(os.Getenv("LLM_TEMPERATURE"), 64)
	if temp == 0 {
		temp = 0.7
	}

	cfg := &Config{
		AppName:         getenv("APP_NAME", "Go Eat"),
		ListenAddr:      getenv("LISTEN_ADDR", ":8080"),
		PublicBaseURL:   getenv("PUBLIC_BASE_URL", ""),
		DatabaseURL:     getenv("DATABASE_URL", "file:./data/goeat.db"),
		SessionSecret:   os.Getenv("SESSION_SECRET"),
		SessionTTLHours: sessionTTL,

		Provider:        getenv("PROVIDER", "openai_compatible"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:  getenv("ANTHROPIC_MODEL", "claude-sonnet-5"),
		LLMAPIUrl:       os.Getenv("LLM_API_URL"),
		LLMModel:        os.Getenv("LLM_MODEL"),
		LLMAPIKey:       os.Getenv("LLM_API_KEY"),
		LLMMaxTokens:    maxToks,
		LLMTemp:         temp,
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
