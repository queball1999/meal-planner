package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"goeat/config"
)

// ProviderInfo describes one selectable AI backend: what to call it, where it
// lives by default, and a curated model list used when the provider's own
// /models endpoint is unreachable (or the key is not set yet).
type ProviderInfo struct {
	ID         string
	Label      string
	DefaultURL string // pre-filled API URL; "" when the provider has no URL field
	ShowURL    bool   // whether the API URL is worth showing/editing
	RequireURL bool   // this provider is unusable without a URL
	DocsURL    string
	Models     []string
}

// Providers is the ordered list shown in the Settings provider picker.
var Providers = []ProviderInfo{
	{
		ID: "anthropic", Label: "Anthropic",
		DocsURL: "https://console.anthropic.com/settings/keys",
		Models: []string{
			"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5-20251001",
		},
	},
	{
		ID: "openai", Label: "OpenAI",
		DefaultURL: "https://api.openai.com/v1", ShowURL: true,
		DocsURL: "https://platform.openai.com/api-keys",
		Models: []string{
			"gpt-5", "gpt-5-mini", "gpt-4.1", "gpt-4.1-mini", "o4-mini",
		},
	},
	{
		ID: "google", Label: "Google Gemini",
		DefaultURL: "https://generativelanguage.googleapis.com/v1beta/openai", ShowURL: true,
		DocsURL: "https://aistudio.google.com/apikey",
		// gemini-2.0-flash and gemini-2.5-pro were both retired in quick
		// succession (their 404s pointed callers at gemini-3.6-flash and
		// gemini-3.1-pro-preview respectively). This curated list is only a
		// fallback for when the live /models call fails, so it will keep
		// drifting behind Google's actual catalog - it is guesswork, not
		// verified against a real account. The gemma-4-31b/-26b entries this
		// list previously carried turned out not to exist ("is not found for
		// API version v1main"), confirming that: "Refresh list" against the
		// real endpoint is the only reliable source for what an account can
		// actually use, this list is a last resort when that call fails.
		Models: []string{
			"gemini-3.1-pro-preview", "gemini-2.5-flash", "gemini-3.6-flash",
		},
	},
	{
		ID: "openai_compatible", Label: "OpenAI-compatible",
		DefaultURL: "http://localhost:11434/v1", ShowURL: true, RequireURL: true,
		DocsURL: "",
		Models:  []string{"llama3.2", "qwen2.5", "mistral"},
	},
}

// ProviderByID returns the catalog entry for id.
func ProviderByID(id string) (ProviderInfo, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// ProviderCreds pulls the API key, base URL, and model for one provider out of
// cfg. It falls back to the legacy LLM_API_* fields when the per-provider
// values are unset, so an .env written before per-provider settings existed
// still resolves.
func ProviderCreds(cfg *config.Config, id string) (apiKey, baseURL, model string) {
	info, _ := ProviderByID(id)
	switch id {
	case "anthropic":
		return cfg.AnthropicAPIKey, "", cfg.AnthropicModel
	case "openai":
		apiKey, baseURL, model = cfg.OpenAIAPIKey, cfg.OpenAIAPIURL, cfg.OpenAIModel
	case "google":
		apiKey, baseURL, model = cfg.GoogleAPIKey, cfg.GoogleAPIURL, cfg.GoogleModel
	default:
		apiKey, baseURL, model = cfg.LLMAPIKey, cfg.LLMAPIUrl, cfg.LLMModel
	}
	if cfg.Provider == id {
		if apiKey == "" {
			apiKey = cfg.LLMAPIKey
		}
		if model == "" {
			model = cfg.LLMModel
		}
		if baseURL == "" {
			baseURL = cfg.LLMAPIUrl
		}
	}
	if baseURL == "" {
		baseURL = info.DefaultURL
	}
	return apiKey, baseURL, model
}

// Configured reports whether a provider has enough set to be usable, which is
// what the Settings picker badges as "ready".
func Configured(cfg *config.Config, id string) bool {
	apiKey, baseURL, _ := ProviderCreds(cfg, id)
	if id == "openai_compatible" {
		return baseURL != ""
	}
	return apiKey != ""
}

// ListModels asks a provider for the models the given key can actually use.
// It returns the provider's own list; callers fall back to ProviderInfo.Models
// when this errors (no key yet, offline endpoint, provider without a /models
// route).
func ListModels(ctx context.Context, id, apiKey, baseURL string) ([]string, error) {
	info, ok := ProviderByID(id)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	if baseURL == "" {
		baseURL = info.DefaultURL
	}

	var url string
	req := func() (*http.Request, error) { return nil, nil }

	if id == "anthropic" {
		url = "https://api.anthropic.com/v1/models?limit=100"
		req = func() (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			r.Header.Set("x-api-key", apiKey)
			r.Header.Set("anthropic-version", "2023-06-01")
			return r, nil
		}
		if apiKey == "" {
			return nil, fmt.Errorf("API key required to list models")
		}
	} else {
		if baseURL == "" {
			return nil, fmt.Errorf("API URL required to list models")
		}
		url = APIBase(baseURL, id) + "/models"
		req = func() (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			if apiKey != "" {
				r.Header.Set("Authorization", "Bearer "+apiKey)
			}
			return r, nil
		}
	}

	httpReq, err := req()
	if err != nil {
		return nil, err
	}
	cli := &http.Client{Timeout: 20 * time.Second}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, snippet(string(body)))
	}

	// Both the OpenAI and Anthropic shapes are {"data":[{"id":"..."}]}.
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("unexpected response from %s: %s", url, snippet(string(body)))
	}

	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s listed no models", url)
	}
	sort.Strings(out)
	return out, nil
}

// APIBase normalises a configured base URL to the version root that the chat
// and models endpoints hang off, so both "http://localhost:11434" and
// "http://localhost:11434/v1" resolve to the same place.
func APIBase(baseURL, provider string) string {
	b := strings.TrimRight(baseURL, "/")
	if b == "" {
		return ""
	}
	if provider == "google" ||
		strings.HasSuffix(b, "/v1") ||
		strings.HasSuffix(b, "/openai") ||
		strings.Contains(b, "/v1beta") {
		return b
	}
	return b + "/v1"
}

func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
