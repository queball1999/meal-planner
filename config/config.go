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
	// OpenAI (api.openai.com, or a drop-in replacement)
	OpenAIAPIKey string
	OpenAIModel  string
	OpenAIAPIURL string
	// Google Gemini via its OpenAI-compatible endpoint
	GoogleAPIKey string
	GoogleModel  string
	GoogleAPIURL string
	// Generic OpenAI-compatible endpoint (Ollama, LM Studio, vLLM, ...)
	LLMAPIUrl string
	LLMModel  string
	LLMAPIKey string
	// Shared sampling parameters, applied to every provider that accepts them.
	// top_k and min_p are not part of the OpenAI API proper, so they are only
	// sent to openai_compatible endpoints (llama.cpp, vLLM, Ollama, ...) and
	// to Anthropic (top_k only).
	LLMMaxTokens int     // default 4096
	LLMTemp      float64 // default 1.01
	LLMTopP      float64 // default 0.91
	LLMTopK      int     // default 20
	LLMMinP      float64 // default 0.1
	LLMPresence  float64 // presence_penalty, default 1.52

	// ── Pricing providers (§11.3) ─────────────────────────────────────────
	PriceCacheTTLHours int // default 168 (1 week); 0 = no expiry

	// Calendar (SS11.1)
	WeekStartDay string // "sunday" (default) | "monday"
	// Kroger OfficialAPIProvider (§6.2)
	KrogerClientID     string
	KrogerClientSecret string
	KrogerLocationID   string

	// FlareSolverr proxy for scraper (§6.7); empty = disabled.
	// Kept as its own field for backwards compatibility with existing .env
	// files; it seeds RenderBackend/RenderURL when those are unset.
	FlareSolverrURL string

	// Headless-browser renderer used when a direct fetch is blocked or the
	// page renders its prices in JavaScript (§6.7).
	//   RenderBackend: "" (disabled) | "flaresolverr" | "browserless"
	//   RenderURL:     base URL of that service
	//   RenderToken:   optional auth token (Browserless)
	RenderBackend string
	RenderURL     string
	RenderToken   string

	// RecipeImageDir is the writable directory for downloaded recipe images (§5.7).
	// Served at /recipe-images/{name}. If empty, image download is skipped.
	RecipeImageDir string
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
	temp := floatEnv("LLM_TEMPERATURE", 1.01)
	topP := floatEnv("LLM_TOP_P", 0.91)
	minP := floatEnv("LLM_MIN_P", 0.1)
	presence := floatEnv("LLM_PRESENCE_PENALTY", 1.52)
	topK := 20
	if v, err := strconv.Atoi(os.Getenv("LLM_TOP_K")); err == nil {
		topK = v
	}

	// Per-provider credentials are the current shape; the older single
	// LLM_API_KEY/LLM_MODEL pair is still honoured as the fallback for
	// whichever provider PROVIDER names, so existing .env files keep working.
	provider := getenv("PROVIDER", "openai_compatible")
	openaiKey, openaiModel, openaiURL := os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_MODEL"), ""
	googleKey, googleModel, googleURL := os.Getenv("GOOGLE_API_KEY"), os.Getenv("GOOGLE_MODEL"), ""
	switch provider {
	case "openai":
		openaiKey = firstNonEmpty(openaiKey, os.Getenv("LLM_API_KEY"))
		openaiModel = firstNonEmpty(openaiModel, os.Getenv("LLM_MODEL"))
		openaiURL = os.Getenv("LLM_API_URL")
	case "google":
		googleKey = firstNonEmpty(googleKey, os.Getenv("LLM_API_KEY"))
		googleModel = firstNonEmpty(googleModel, os.Getenv("LLM_MODEL"))
		googleURL = os.Getenv("LLM_API_URL")
	}

	cfg := &Config{
		AppName:         getenv("APP_NAME", "Go Eat"),
		ListenAddr:      getenv("LISTEN_ADDR", ":8080"),
		PublicBaseURL:   getenv("PUBLIC_BASE_URL", ""),
		DatabaseURL:     getenv("DATABASE_URL", "file:./data/goeat.db"),
		SessionSecret:   os.Getenv("SESSION_SECRET"),
		SessionTTLHours: sessionTTL,

		Provider:        provider,
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:  getenv("ANTHROPIC_MODEL", "claude-sonnet-5"),
		OpenAIAPIKey:    openaiKey,
		OpenAIModel:     openaiModel,
		OpenAIAPIURL:    getenv("OPENAI_API_URL", openaiURL),
		GoogleAPIKey:    googleKey,
		GoogleModel:     googleModel,
		GoogleAPIURL:    getenv("GOOGLE_API_URL", googleURL),
		LLMAPIUrl:       os.Getenv("LLM_API_URL"),
		LLMModel:        os.Getenv("LLM_MODEL"),
		LLMAPIKey:       os.Getenv("LLM_API_KEY"),
		LLMMaxTokens:    maxToks,
		LLMTemp:         temp,
		LLMTopP:         topP,
		LLMTopK:         topK,
		LLMMinP:         minP,
		LLMPresence:     presence,

		PriceCacheTTLHours: func() int {
			v, _ := strconv.Atoi(os.Getenv("PRICE_CACHE_TTL_HOURS"))
			if v <= 0 {
				return 168
			}
			return v
		}(),
		KrogerClientID:     os.Getenv("KROGER_CLIENT_ID"),
		KrogerClientSecret: os.Getenv("KROGER_CLIENT_SECRET"),
		KrogerLocationID:   os.Getenv("KROGER_LOCATION_ID"),
		FlareSolverrURL:    os.Getenv("FLARESOLVERR_URL"),
		RenderBackend:      os.Getenv("RENDER_BACKEND"),
		RenderURL:          os.Getenv("RENDER_URL"),
		RenderToken:        os.Getenv("RENDER_TOKEN"),
		RecipeImageDir:     getenv("RECIPE_IMAGE_DIR", "./data/recipe-images"),

		WeekStartDay: func() string {
			d := os.Getenv("WEEK_START_DAY")
			if d == "monday" {
				return "monday"
			}
			return "sunday"
		}(),
	}

	if cfg.SessionSecret == "" {
		// Dev fallback: ephemeral secret with a loud warning. Set SESSION_SECRET
		// in .env for any persistent deployment.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("SESSION_SECRET not set and could not generate one: %w", err)
		}
		cfg.SessionSecret = hex.EncodeToString(b)
		log.Println("WARNING: SESSION_SECRET not set - using an ephemeral secret. Sessions will not survive restarts. Set SESSION_SECRET in .env.")
	}

	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// floatEnv parses a float env var, falling back to def when unset or unparseable.
// An explicit "0" is honoured - only a missing or malformed value takes def.
func floatEnv(key string, def float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

// firstNonEmpty returns the first argument that is not the empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
