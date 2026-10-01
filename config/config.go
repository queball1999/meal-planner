package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration. Values are loaded from .env (if
// present) then from environment variables. SESSION_SECRET, DATABASE_URL, and
// LISTEN_ADDR are not runtime-editable (§11.4).
type Config struct {
	AppName       string
	ListenAddr    string
	PublicBaseURL string
	DatabaseURL   string
	SessionSecret string
	// EphemeralSecret is set when SESSION_SECRET was missing and Load made up
	// a one-boot secret. Anything sealed with it is unreadable after a
	// restart, so secret settings are then stored unsealed.
	EphemeralSecret bool
	// Desktop is set (GOEAT_DESKTOP=1) when the Tauri shell runs this binary
	// as its sidecar: listen on loopback only, announce the bound address on
	// stdout, accept only loopback Host headers, and exit when stdin closes.
	Desktop bool
	// SetupToken, when set, is the token the first-run wizard demands
	// instead of a freshly generated one (QSS security design §15). At
	// least 24 hex characters; Load rejects anything weaker.
	SetupToken      string
	SessionTTLHours int // default 168 (7 days)
	// SessionIdleMinutes (SESSION_IDLE_MINUTES, default 1440) signs a session
	// out after this long with no page loads, enforced by the server. 0 turns
	// the idle limit off; SessionTTLHours still caps the session's life.
	SessionIdleMinutes int
	// TrustedProxies (TRUSTED_PROXIES) are the CIDRs of reverse proxies whose
	// X-Forwarded-For is believed. Empty: clients connect directly and the
	// header is ignored, so nobody can claim someone else's IP (QSS §5.4).
	TrustedProxies []netip.Prefix
	// AllowedHosts (ALLOWED_HOSTS) are extra Host header values accepted
	// besides PUBLIC_BASE_URL's host. Any other Host is refused, so a
	// DNS-rebinding page can't read this server's responses (QSS §9.3).
	AllowedHosts []string
	// MultiTenant (ENABLE_MULTI_TENANT, default off) allows more than one
	// household on this server: the household switcher, and creating and
	// deleting households. Off, everyone works in the one household setup
	// created; household roles (owner/editor/viewer) apply either way.
	MultiTenant bool
	// UpdateCheck (UPDATE_CHECK, default on) asks GitHub twice a day whether
	// a newer stable release is out (phase 15). Read-only; never downloads.
	UpdateCheck bool

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
	LLMMaxTokens int // default 4096
	// LLMPlanMaxTokens is the output budget for each plan-generation call -
	// a whole week of meals in one JSON reply needs far more than
	// LLMMaxTokens. Default 16384.
	LLMPlanMaxTokens int
	LLMTemp          float64 // default 1.01
	LLMTopP          float64 // default 0.91
	LLMTopK          int     // default 20
	LLMMinP          float64 // default 0.1
	LLMPresence      float64 // presence_penalty, default 1.52

	// ── Pricing providers (§11.3) ─────────────────────────────────────────
	PriceCacheTTLHours int // default 168 (1 week); 0 = no expiry

	// Calendar (SS11.1)
	WeekStartDay string // "sunday" (default) | "monday"
	// Timezone is the IANA zone (e.g. "America/Chicago") every timestamp in
	// the UI is shown in. Default "UTC". Households keep their own zone for
	// the auto-plan schedule; this one is only for display.
	Timezone string
	// AutoPlanHour is the local hour (0-23, household timezone) the night
	// before the week starts to automatically generate next week's plan.
	// -1 (default) disables auto-generation; the manual generate/regenerate
	// button always works regardless. The trigger day is always Saturday -
	// plan.Generate's week always starts the following Sunday.
	AutoPlanHour int
	// Kroger OfficialAPIProvider (§6.2)
	KrogerClientID     string
	KrogerClientSecret string
	KrogerLocationID   string
	// KrogerCredentials is the parsed, position-aligned list of
	// {clientID, clientSecret} pairs from the comma-separated KROGER_CLIENT_ID
	// and KROGER_CLIENT_SECRET values. More than one pair enables automatic
	// credential rotation when one key is rate-limited or spends its daily
	// quota. Rebuilt by DeriveKrogerCredentials (called by Load and by
	// settings.Apply).
	KrogerCredentials [][2]string
	// KrogerMaxRPM caps requests per minute to the Kroger API. Default 60.
	KrogerMaxRPM int
	// KrogerDailyCap is the per-credential daily call budget before the client
	// rotates to the next key. Default 9500, a margin under Kroger's 10k/day
	// Products limit.
	KrogerDailyCap int

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

	// ItemImageDir is the writable directory for catalog-item images
	// (00010_items.sql). Served at /item-images/{name}. If empty, image
	// download is skipped.
	ItemImageDir string

	// ── Home Assistant shopping-list sync (00012_ha_sync.sql) ─────────────
	// HAToken is normally set from the Settings page (encrypted at rest via
	// package cryptbox); an .env value is honoured as a fallback.
	HABaseURL             string
	HAToken               string
	HATodoEntity          string // default "todo.shopping_list"
	HASyncIntervalMinutes int    // 0 = pull disabled
	HAItemFormat          string // "name" | "name_qty" (default)
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
	planToks, _ := strconv.Atoi(os.Getenv("LLM_PLAN_MAX_TOKENS"))
	if planToks <= 0 {
		planToks = 16384
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

		Provider:         provider,
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:   getenv("ANTHROPIC_MODEL", "claude-sonnet-5"),
		OpenAIAPIKey:     openaiKey,
		OpenAIModel:      openaiModel,
		OpenAIAPIURL:     getenv("OPENAI_API_URL", openaiURL),
		GoogleAPIKey:     googleKey,
		GoogleModel:      googleModel,
		GoogleAPIURL:     getenv("GOOGLE_API_URL", googleURL),
		LLMAPIUrl:        os.Getenv("LLM_API_URL"),
		LLMModel:         os.Getenv("LLM_MODEL"),
		LLMAPIKey:        os.Getenv("LLM_API_KEY"),
		LLMMaxTokens:     maxToks,
		LLMPlanMaxTokens: planToks,
		LLMTemp:          temp,
		LLMTopP:          topP,
		LLMTopK:          topK,
		LLMMinP:          minP,
		LLMPresence:      presence,

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
		KrogerMaxRPM:       intEnv("KROGER_MAX_RPM", 60),
		KrogerDailyCap:     intEnv("KROGER_DAILY_CAP", 9500),
		FlareSolverrURL:    os.Getenv("FLARESOLVERR_URL"),
		RenderBackend:      os.Getenv("RENDER_BACKEND"),
		RenderURL:          os.Getenv("RENDER_URL"),
		RenderToken:        os.Getenv("RENDER_TOKEN"),
		RecipeImageDir:     getenv("RECIPE_IMAGE_DIR", "./data/recipe-images"),
		ItemImageDir:       getenv("ITEM_IMAGE_DIR", "./data/item-images"),

		HABaseURL:    os.Getenv("HA_BASE_URL"),
		HAToken:      os.Getenv("HA_TOKEN"),
		HATodoEntity: getenv("HA_TODO_ENTITY", "todo.shopping_list"),
		HASyncIntervalMinutes: func() int {
			v, _ := strconv.Atoi(os.Getenv("HA_SYNC_INTERVAL_MINUTES"))
			if v < 0 {
				return 0
			}
			return v
		}(),
		HAItemFormat: getenv("HA_ITEM_FORMAT", "name_qty"),

		Timezone: func() string {
			tz := getenv("APP_TIMEZONE", "UTC")
			if _, err := time.LoadLocation(tz); err != nil {
				return "UTC"
			}
			return tz
		}(),
		WeekStartDay: func() string {
			d := os.Getenv("WEEK_START_DAY")
			if d == "monday" {
				return "monday"
			}
			return "sunday"
		}(),
		AutoPlanHour: func() int {
			v, err := strconv.Atoi(os.Getenv("AUTO_PLAN_HOUR"))
			if err != nil || v < 0 || v > 23 {
				return -1
			}
			return v
		}(),
	}

	cfg.DeriveKrogerCredentials()

	cfg.MultiTenant, _ = strconv.ParseBool(os.Getenv("ENABLE_MULTI_TENANT"))
	cfg.UpdateCheck = ParseOnOff(os.Getenv("UPDATE_CHECK"), true)

	cfg.Desktop = os.Getenv("GOEAT_DESKTOP") == "1"
	if cfg.Desktop && !loopbackAddr(cfg.ListenAddr) {
		return nil, fmt.Errorf("GOEAT_DESKTOP=1 requires a loopback LISTEN_ADDR (e.g. 127.0.0.1:0), got %q", cfg.ListenAddr)
	}

	cfg.SessionIdleMinutes = 1440
	if v, err := strconv.Atoi(os.Getenv("SESSION_IDLE_MINUTES")); err == nil && v >= 0 {
		cfg.SessionIdleMinutes = v
	}

	proxies, err := ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return nil, err
	}
	cfg.TrustedProxies = proxies
	if len(proxies) > 0 {
		log.Printf("config: trusting X-Forwarded-For from %v", proxies)
	}

	for _, h := range splitList(os.Getenv("ALLOWED_HOSTS")) {
		if h = strings.ToLower(strings.TrimSuffix(h, "/")); h != "" {
			cfg.AllowedHosts = append(cfg.AllowedHosts, h)
		}
	}

	cfg.SetupToken = strings.TrimSpace(os.Getenv("SETUP_TOKEN"))
	if cfg.SetupToken != "" && !validSetupToken(cfg.SetupToken) {
		return nil, fmt.Errorf("SETUP_TOKEN must be at least 24 hex characters (generate one with: openssl rand -hex 16)")
	}

	// The .env.example placeholder is public, so a server started with it has
	// a secret anyone can read: it keys the session cookies and the sealed
	// API keys (QSS security design §19). Refuse it outright.
	if cfg.SessionSecret == sessionSecretPlaceholder {
		return nil, fmt.Errorf("SESSION_SECRET is still the .env.example placeholder - set a random one (generate with: openssl rand -hex 32)")
	}

	if cfg.SessionSecret == "" {
		// Dev fallback: ephemeral secret with a loud warning. Set SESSION_SECRET
		// in .env for any persistent deployment.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("SESSION_SECRET not set and could not generate one: %w", err)
		}
		cfg.SessionSecret = hex.EncodeToString(b)
		cfg.EphemeralSecret = true
		log.Println("WARNING: SESSION_SECRET not set - using an ephemeral secret. Sessions will not survive restarts. Set SESSION_SECRET in .env.")
	}

	return cfg, nil
}

// sessionSecretPlaceholder is the SESSION_SECRET value shipped in
// .env.example. Keep the two in step.
const sessionSecretPlaceholder = "change-me-to-a-random-32-char-secret"

// ParseTrustedProxies parses a comma-separated CIDR list. A bare IP is
// taken as a single-address prefix. Any bad entry fails startup, since a
// typo here would silently trust (or distrust) the wrong peer.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range splitList(raw) {
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			addr, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP or CIDR", part)
			}
			out = append(out, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP or CIDR", part)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// HostAllowlist returns every Host header value the server answers to:
// PUBLIC_BASE_URL's host plus ALLOWED_HOSTS. Empty means no list has been
// configured, and Host isn't checked.
func (c *Config) HostAllowlist() []string {
	var out []string
	if u, err := url.Parse(strings.TrimSpace(c.PublicBaseURL)); err == nil && u.Host != "" {
		out = append(out, strings.ToLower(u.Host))
	}
	return append(out, c.AllowedHosts...)
}

// loopbackAddr reports whether a host:port listen address is a loopback IP -
// a desktop app's server must never be reachable from the network.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validSetupToken: at least 24 hex characters (96 bits).
func validSetupToken(tok string) bool {
	return len(tok) >= 24 && strings.Trim(tok, "0123456789abcdefABCDEF") == ""
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ParseOnOff reads an on/off switch: "on"/"off" (the Settings page's values)
// or anything strconv.ParseBool takes. Blank or unrecognised gives def.
func ParseOnOff(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on":
		return true
	case "off":
		return false
	}
	if v, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
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

// intEnv parses an int env var, falling back to def when unset or non-positive.
func intEnv(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// splitList splits a comma-separated value into trimmed parts, preserving
// position (empty segments are kept so index alignment across two lists holds).
// A blank input yields nil.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// DeriveKrogerCredentials rebuilds KrogerCredentials by zipping the
// comma-separated KrogerClientID and KrogerClientSecret lists
// position-for-position. The common single-credential case (no comma on either
// side) yields one pair. Call it after either field is assigned.
func (c *Config) DeriveKrogerCredentials() {
	ids, secrets := splitList(c.KrogerClientID), splitList(c.KrogerClientSecret)
	n := len(ids)
	if len(secrets) < n {
		n = len(secrets)
	}
	if len(ids) != len(secrets) && len(ids) > 0 && len(secrets) > 0 {
		log.Printf("config: KROGER_CLIENT_ID has %d entries but KROGER_CLIENT_SECRET has %d; using the first %d pair(s)",
			len(ids), len(secrets), n)
	}
	creds := make([][2]string, 0, n)
	for i := 0; i < n; i++ {
		if ids[i] == "" || secrets[i] == "" {
			continue
		}
		creds = append(creds, [2]string{ids[i], secrets[i]})
	}
	c.KrogerCredentials = creds
}
