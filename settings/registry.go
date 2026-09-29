// Package settings makes the subset of config.Config that is safe to edit
// without touching the shell environment available in the Settings page.
//
// Three fields are deliberately excluded from Defs and can only ever be set
// via .env: SessionSecret, DatabaseURL, and ListenAddr (see the comment on
// config.Config - §11.4). Editing them at runtime is either dangerous
// (rotating the session secret while sessions are open) or meaningless
// (the listen address and DB file are fixed for the life of the process).
//
// Every other field is constructed once at startup - into the LLM generator,
// the pricing chain, or a Server field - so an edit here always needs a
// restart to take effect. That is a real limitation of this codebase, not a
// missing feature of this package: Apply runs once, before those subsystems
// are built, exactly like the one-time env load it replaces.
package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"goeat/config"
	"goeat/db"
)

// Kind controls how a Definition's value is parsed, validated, and rendered.
type Kind int

const (
	KindString Kind = iota
	KindInt
	KindFloat
	KindSelect
	KindSecret // rendered as a password input; never sent back to the browser
)

// Definition describes one editable setting. Key doubles as the .env
// variable name and the settings table's primary key, so there is exactly
// one name to look up in .env.example, this file, and the DB.
type Definition struct {
	Key   string
	Label string
	Help  string
	// Tooltip, when set, adds a "(?)" help popup beside the label for
	// longer how-to text that would crowd the always-visible Help line.
	Tooltip  string
	Category string
	Kind     Kind
	Options  []string // choices for KindSelect

	// Provider tags a field as belonging to one AI backend, so the Settings
	// page can show only the selected provider's credentials while every
	// provider keeps its own stored values. Empty means the field applies to
	// all providers (or is not an AI field at all).
	Provider string
	// Role identifies an AI field's purpose to the provider panel:
	// "key" | "model" | "url". Empty for everything else.
	Role string

	// Encrypted marks a KindSecret whose value is stored in the `secrets`
	// table (cryptbox-sealed) instead of the plaintext `settings` table.
	Encrypted bool

	// FromConfig returns the setting's current value (from cfg, i.e. from
	// .env) as a string, used to seed the settings table on first boot.
	FromConfig func(cfg *config.Config) string
}

// Defs is the ordered list of editable settings, grouped by Category in
// display order.
var Defs = []Definition{
	{Key: "APP_NAME", Label: "App name", Category: "General", Kind: KindString,
		Help:       "Shown in the header and page titles.",
		FromConfig: func(c *config.Config) string { return c.AppName }},
	{Key: "PUBLIC_BASE_URL", Label: "Public base URL", Category: "General", Kind: KindString,
		Help:       "Canonical URL used in redirects and CSRF origin checks (needed when Docker maps a different host port).",
		FromConfig: func(c *config.Config) string { return c.PublicBaseURL }},
	{Key: "SESSION_TTL_HOURS", Label: "Session lifetime (hours)", Category: "General", Kind: KindInt,
		Help:       "How long a signed-in session stays valid. Default 168 (7 days).",
		FromConfig: func(c *config.Config) string { return strconv.Itoa(c.SessionTTLHours) }},

	{Key: "PROVIDER", Label: "Default provider", Category: "AI Provider", Kind: KindSelect,
		Options:    []string{"anthropic", "openai", "google", "openai_compatible"},
		Help:       "Which configured backend actually generates meal plans.",
		FromConfig: func(c *config.Config) string { return c.Provider }},

	{Key: "ANTHROPIC_API_KEY", Label: "API key", Category: "AI Provider", Kind: KindSecret,
		Provider: "anthropic", Role: "key",
		Help:       "From console.anthropic.com - API keys.",
		FromConfig: func(c *config.Config) string { return c.AnthropicAPIKey }},
	{Key: "ANTHROPIC_MODEL", Label: "Model", Category: "AI Provider", Kind: KindString,
		Provider: "anthropic", Role: "model",
		Help:       "e.g. claude-sonnet-5.",
		FromConfig: func(c *config.Config) string { return c.AnthropicModel }},

	{Key: "OPENAI_API_KEY", Label: "API key", Category: "AI Provider", Kind: KindSecret,
		Provider: "openai", Role: "key",
		Help:       "From platform.openai.com - API keys.",
		FromConfig: func(c *config.Config) string { return c.OpenAIAPIKey }},
	{Key: "OPENAI_MODEL", Label: "Model", Category: "AI Provider", Kind: KindString,
		Provider: "openai", Role: "model",
		FromConfig: func(c *config.Config) string { return c.OpenAIModel }},
	{Key: "OPENAI_API_URL", Label: "API URL", Category: "AI Provider", Kind: KindString,
		Provider: "openai", Role: "url",
		Help:       "Leave as api.openai.com unless you point at a drop-in replacement.",
		FromConfig: func(c *config.Config) string { return c.OpenAIAPIURL }},

	{Key: "GOOGLE_API_KEY", Label: "API key", Category: "AI Provider", Kind: KindSecret,
		Provider: "google", Role: "key",
		Help:       "From aistudio.google.com - Get API key.",
		FromConfig: func(c *config.Config) string { return c.GoogleAPIKey }},
	{Key: "GOOGLE_MODEL", Label: "Model", Category: "AI Provider", Kind: KindString,
		Provider: "google", Role: "model",
		FromConfig: func(c *config.Config) string { return c.GoogleModel }},
	{Key: "GOOGLE_API_URL", Label: "API URL", Category: "AI Provider", Kind: KindString,
		Provider: "google", Role: "url",
		Help:       "Gemini's OpenAI-compatible endpoint.",
		FromConfig: func(c *config.Config) string { return c.GoogleAPIURL }},

	{Key: "LLM_API_KEY", Label: "API key", Category: "AI Provider", Kind: KindSecret,
		Provider: "openai_compatible", Role: "key",
		Help:       "Often blank for a local server (Ollama, LM Studio, llama.cpp).",
		FromConfig: func(c *config.Config) string { return c.LLMAPIKey }},
	{Key: "LLM_MODEL", Label: "Model", Category: "AI Provider", Kind: KindString,
		Provider: "openai_compatible", Role: "model",
		FromConfig: func(c *config.Config) string { return c.LLMModel }},
	{Key: "LLM_API_URL", Label: "API URL", Category: "AI Provider", Kind: KindString,
		Provider: "openai_compatible", Role: "url",
		Help:       "Base URL of the endpoint, e.g. http://localhost:11434/v1.",
		FromConfig: func(c *config.Config) string { return c.LLMAPIUrl }},

	{Key: "LLM_MAX_TOKENS", Label: "Max tokens", Category: "AI Provider", Kind: KindInt,
		Help:       "Upper bound on generated tokens per call. Default 4096.",
		FromConfig: func(c *config.Config) string { return strconv.Itoa(c.LLMMaxTokens) }},
	{Key: "LLM_TEMPERATURE", Label: "Temperature", Category: "AI Provider", Kind: KindFloat,
		Help:       "Sampling temperature. Default 1.01 (clamped to 1 for Anthropic).",
		FromConfig: func(c *config.Config) string { return formatFloat(c.LLMTemp) }},
	{Key: "LLM_TOP_P", Label: "Top P", Category: "AI Provider", Kind: KindFloat,
		Help:       "Nucleus sampling cutoff, 0-1. Default 0.91.",
		FromConfig: func(c *config.Config) string { return formatFloat(c.LLMTopP) }},
	{Key: "LLM_TOP_K", Label: "Top K", Category: "AI Provider", Kind: KindInt,
		Help:       "Keep only the K most likely tokens. Default 20. Sent to OpenAI-compatible and Anthropic backends only.",
		FromConfig: func(c *config.Config) string { return strconv.Itoa(c.LLMTopK) }},
	{Key: "LLM_MIN_P", Label: "Min P", Category: "AI Provider", Kind: KindFloat,
		Help:       "Drop tokens below this share of the top token's probability. Default 0.1. OpenAI-compatible backends only.",
		FromConfig: func(c *config.Config) string { return formatFloat(c.LLMMinP) }},
	{Key: "LLM_PRESENCE_PENALTY", Label: "Presence penalty", Category: "AI Provider", Kind: KindFloat,
		Help:       "Discourages repeating tokens already used. Default 1.52. Not sent to Google Gemini - some Gemini models reject any nonzero value.",
		FromConfig: func(c *config.Config) string { return formatFloat(c.LLMPresence) }},

	{Key: "PRICE_CACHE_TTL_HOURS", Label: "Price cache lifetime (hours)", Category: "Pricing", Kind: KindInt,
		Help:       "How long a cached grocery price stays valid before it's re-fetched. Default 168 (1 week); 0 = never expires.",
		FromConfig: func(c *config.Config) string { return strconv.Itoa(c.PriceCacheTTLHours) }},

	{Key: "WEEK_START_DAY", Label: "Week starts on", Category: "Calendar", Kind: KindSelect,
		Options:    []string{"sunday", "monday"},
		Help:       "First day of the planning week.",
		FromConfig: func(c *config.Config) string { return c.WeekStartDay }},
	{Key: "AUTO_PLAN_HOUR", Label: "Auto-generate hour", Category: "Calendar", Kind: KindInt,
		Help: "Local hour (0-23) the Saturday night before the week starts to automatically generate " +
			"next week's plan. Set to -1 to disable auto-generation - the Regenerate/Plan my week " +
			"button in the app always works regardless.",
		FromConfig: func(c *config.Config) string { return strconv.Itoa(c.AutoPlanHour) }},

	{Key: "KROGER_CLIENT_ID", Label: "Client ID", Category: "Kroger", Kind: KindString,
		Help: "Enables live Kroger pricing when set along with Client secret and Location ID. " +
			"Comma-separate several Client IDs (with a matching comma-separated Client secret list) " +
			"to rotate credentials automatically when one is rate-limited or hits its daily quota.",
		FromConfig: func(c *config.Config) string { return c.KrogerClientID }},
	{Key: "KROGER_CLIENT_SECRET", Label: "Client secret", Category: "Kroger", Kind: KindSecret,
		Help:       "One secret, or a comma-separated list aligned position-for-position with Client ID.",
		FromConfig: func(c *config.Config) string { return c.KrogerClientSecret }},
	{Key: "KROGER_LOCATION_ID", Label: "Location ID", Category: "Kroger", Kind: KindString,
		Help: "The Kroger store location to price against.",
		Tooltip: "The 8-character ID of your Kroger-family store (Kroger, Ralphs, Fred Meyer, King Soopers, " +
			"Smith's, Fry's, etc.). To find it: open kroger.com (or your banner's site), pick your store, " +
			"then open its store details page. The URL ends in two number groups, e.g. " +
			".../stores/details/014/00338 - join them: 01400338. " +
			"Use Test connection below to confirm it resolves to the right store.",
		FromConfig: func(c *config.Config) string { return c.KrogerLocationID }},

	{Key: "FLARESOLVERR_URL", Label: "FlareSolverr URL", Category: "Scraping", Kind: KindString,
		Help: "Solves anti-bot challenges. Used as the second attempt whenever it is set, " +
			"even with Headless browser set to browserless. e.g. http://flaresolverr:8191.",
		FromConfig: func(c *config.Config) string { return c.FlareSolverrURL }},

	{Key: "RENDER_BACKEND", Label: "Headless browser", Category: "Scraping", Kind: KindSelect,
		Options: []string{"", "auto", "flaresolverr", "browserless"},
		Help: "Service used when a store blocks plain HTTP fetches or renders prices in JavaScript. " +
			"auto runs both - Browserless at the URL below, FlareSolverr at the URL above - and picks the " +
			"right one per page, falling back to the other. flaresolverr solves Cloudflare-style challenges; " +
			"browserless runs the page's JavaScript. Empty disables both.",
		FromConfig: func(c *config.Config) string { return c.RenderBackend }},
	{Key: "RENDER_URL", Label: "Headless browser URL", Category: "Scraping", Kind: KindString,
		Help: "Base URL of that service - under auto this is the Browserless one, e.g. " +
			"http://browserless:3000. Inside Docker use the service name, not localhost.",
		FromConfig: func(c *config.Config) string { return c.RenderURL }},
	{Key: "RENDER_TOKEN", Label: "Headless browser token", Category: "Scraping", Kind: KindSecret,
		Help:       "Optional auth token (Browserless TOKEN). Leave empty when the service is unauthenticated.",
		FromConfig: func(c *config.Config) string { return c.RenderToken }},

	{Key: "RECIPE_IMAGE_DIR", Label: "Recipe image directory", Category: "Storage", Kind: KindString,
		Help:       "Writable directory for downloaded recipe images, served at /recipe-images/. Empty disables image download.",
		FromConfig: func(c *config.Config) string { return c.RecipeImageDir }},
	{Key: "ITEM_IMAGE_DIR", Label: "Item image directory", Category: "Storage", Kind: KindString,
		Help:       "Writable directory for catalog-item images, served at /item-images/. Empty disables image download.",
		FromConfig: func(c *config.Config) string { return c.ItemImageDir }},

	{Key: "HA_BASE_URL", Label: "Base URL", Category: "Home Assistant", Kind: KindString,
		Help:       "e.g. http://homeassistant.local:8123 - the address of your Home Assistant instance.",
		FromConfig: func(c *config.Config) string { return c.HABaseURL }},
	{Key: "HA_TOKEN", Label: "Long-lived access token", Category: "Home Assistant", Kind: KindSecret, Encrypted: true,
		Help:       "Create one under your HA profile - Security - Long-lived access tokens. Stored encrypted.",
		FromConfig: func(c *config.Config) string { return c.HAToken }},
	{Key: "HA_TODO_ENTITY", Label: "To-do entity", Category: "Home Assistant", Kind: KindString,
		Help:       "The todo entity to sync with. Default todo.shopping_list.",
		FromConfig: func(c *config.Config) string { return c.HATodoEntity }},
	{Key: "HA_SYNC_INTERVAL_MINUTES", Label: "Pull interval (minutes)", Category: "Home Assistant", Kind: KindInt,
		Help:       "How often to pull completed items back from HA. 0 disables the background pull (push on demand still works).",
		FromConfig: func(c *config.Config) string { return strconv.Itoa(c.HASyncIntervalMinutes) }},
	{Key: "HA_ITEM_FORMAT", Label: "Item text", Category: "Home Assistant", Kind: KindSelect,
		Options:    []string{"name", "name_qty"},
		Help:       "name = just the item; name_qty = quantity + unit + item (e.g. \"2 lb chicken thighs\").",
		FromConfig: func(c *config.Config) string { return c.HAItemFormat }},
}

// ByKey looks up a Definition by its Key, or reports ok=false for an unknown key.
func ByKey(key string) (Definition, bool) {
	for _, d := range Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

// Validate checks value against d.Kind (and any per-key constraints) without
// applying it anywhere. It returns the error a save request should surface.
func Validate(d Definition, value string) error {
	switch d.Kind {
	case KindInt:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("must be a whole number")
		}
	case KindFloat:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("must be a number")
		}
	case KindSelect:
		for _, opt := range d.Options {
			if value == opt {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(d.Options, ", "))
	}
	return nil
}

// Seed inserts every Definition's current .env-derived value into the
// settings table with source 'env', skipping keys that already have a row
// (an earlier boot's env value or an admin edit). Call once at startup,
// before Apply.
func Seed(ctx context.Context, store db.Store, cfg *config.Config) error {
	for _, d := range Defs {
		if err := store.SeedSetting(ctx, d.Key, d.FromConfig(cfg)); err != nil {
			return err
		}
	}
	return nil
}

// Apply overwrites cfg's fields with any admin-sourced settings row, so a
// prior edit takes effect on this boot. Values that fail to parse are
// skipped with a warning rather than aborting startup - the .env default
// stays in effect for that one field. Call once at startup, after Seed and
// before any subsystem that reads cfg is constructed.
func Apply(ctx context.Context, store db.Store, cfg *config.Config, warn func(format string, args ...any)) error {
	rows, err := store.ListSettings(ctx)
	if err != nil {
		return err
	}
	byKey := make(map[string]*db.Setting, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}

	get := func(key string) (string, bool) {
		r := byKey[key]
		if r == nil || r.Source != "admin" {
			return "", false
		}
		return r.Value, true
	}
	warnBad := func(key, value string, err error) {
		warn("settings: stored value for %s (%q) is invalid, keeping .env default: %v", key, value, err)
	}

	if v, ok := get("APP_NAME"); ok {
		cfg.AppName = v
	}
	if v, ok := get("PUBLIC_BASE_URL"); ok {
		cfg.PublicBaseURL = v
	}
	if v, ok := get("SESSION_TTL_HOURS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SessionTTLHours = n
		} else {
			warnBad("SESSION_TTL_HOURS", v, err)
		}
	}
	strFields := map[string]*string{
		"PROVIDER":          &cfg.Provider,
		"ANTHROPIC_API_KEY": &cfg.AnthropicAPIKey,
		"ANTHROPIC_MODEL":   &cfg.AnthropicModel,
		"OPENAI_API_KEY":    &cfg.OpenAIAPIKey,
		"OPENAI_MODEL":      &cfg.OpenAIModel,
		"OPENAI_API_URL":    &cfg.OpenAIAPIURL,
		"GOOGLE_API_KEY":    &cfg.GoogleAPIKey,
		"GOOGLE_MODEL":      &cfg.GoogleModel,
		"GOOGLE_API_URL":    &cfg.GoogleAPIURL,
		"LLM_API_URL":       &cfg.LLMAPIUrl,
		"LLM_MODEL":         &cfg.LLMModel,
		"LLM_API_KEY":       &cfg.LLMAPIKey,
	}
	for key, dst := range strFields {
		if v, ok := get(key); ok {
			*dst = v
		}
	}
	if v, ok := get("LLM_MAX_TOKENS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.LLMMaxTokens = n
		} else {
			warnBad("LLM_MAX_TOKENS", v, err)
		}
	}
	if v, ok := get("LLM_TOP_K"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.LLMTopK = n
		} else {
			warnBad("LLM_TOP_K", v, err)
		}
	}
	floatFields := map[string]*float64{
		"LLM_TEMPERATURE":      &cfg.LLMTemp,
		"LLM_TOP_P":            &cfg.LLMTopP,
		"LLM_MIN_P":            &cfg.LLMMinP,
		"LLM_PRESENCE_PENALTY": &cfg.LLMPresence,
	}
	for key, dst := range floatFields {
		v, ok := get(key)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			warnBad(key, v, err)
			continue
		}
		*dst = f
	}
	if v, ok := get("PRICE_CACHE_TTL_HOURS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.PriceCacheTTLHours = n
		} else {
			warnBad("PRICE_CACHE_TTL_HOURS", v, err)
		}
	}
	if v, ok := get("WEEK_START_DAY"); ok {
		cfg.WeekStartDay = v
	}
	if v, ok := get("AUTO_PLAN_HOUR"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= -1 && n <= 23 {
			cfg.AutoPlanHour = n
		} else {
			warnBad("AUTO_PLAN_HOUR", v, err)
		}
	}
	if v, ok := get("KROGER_CLIENT_ID"); ok {
		cfg.KrogerClientID = v
	}
	if v, ok := get("KROGER_CLIENT_SECRET"); ok {
		cfg.KrogerClientSecret = v
	}
	if v, ok := get("KROGER_LOCATION_ID"); ok {
		cfg.KrogerLocationID = v
	}
	// Rebuild the parsed credential pool from whatever the ID/secret fields
	// now hold (either may have just changed above, or neither).
	cfg.DeriveKrogerCredentials()
	if v, ok := get("FLARESOLVERR_URL"); ok {
		cfg.FlareSolverrURL = v
	}
	if v, ok := get("RENDER_BACKEND"); ok {
		cfg.RenderBackend = v
	}
	if v, ok := get("RENDER_URL"); ok {
		cfg.RenderURL = v
	}
	if v, ok := get("RENDER_TOKEN"); ok {
		cfg.RenderToken = v
	}
	if v, ok := get("RECIPE_IMAGE_DIR"); ok {
		cfg.RecipeImageDir = v
	}
	if v, ok := get("ITEM_IMAGE_DIR"); ok {
		cfg.ItemImageDir = v
	}
	if v, ok := get("HA_BASE_URL"); ok {
		cfg.HABaseURL = v
	}
	if v, ok := get("HA_TODO_ENTITY"); ok {
		cfg.HATodoEntity = v
	}
	if v, ok := get("HA_ITEM_FORMAT"); ok {
		cfg.HAItemFormat = v
	}
	if v, ok := get("HA_SYNC_INTERVAL_MINUTES"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.HASyncIntervalMinutes = n
		} else {
			warnBad("HA_SYNC_INTERVAL_MINUTES", v, err)
		}
	}

	return nil
}

// formatFloat renders a float setting without a trailing ".0" or an exponent,
// so a seeded value reads the same here as it does in .env.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
