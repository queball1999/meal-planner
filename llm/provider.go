package llm

import (
	"fmt"

	"goeat/config"
)

// NewGenerator returns a Generator for cfg.Provider, the provider selected as
// the default in Settings. Credentials come from that provider's own fields
// (see ProviderCreds), so every configured provider keeps its key and model
// and switching the default is a one-field change.
// Returns an error if the selected provider is missing what it needs.
func NewGenerator(cfg *config.Config) (Generator, error) {
	id := cfg.Provider
	if id == "" {
		id = "openai_compatible"
	}
	info, ok := ProviderByID(id)
	if !ok {
		return nil, fmt.Errorf("unknown LLM provider %q (valid: anthropic, openai, google, openai_compatible)", cfg.Provider)
	}

	sampling := Sampling{
		Temperature: cfg.LLMTemp,
		TopP:        cfg.LLMTopP,
		TopK:        cfg.LLMTopK,
		MinP:        cfg.LLMMinP,
		Presence:    cfg.LLMPresence,
	}

	apiKey, baseURL, model := ProviderCreds(cfg, id)
	if model == "" && len(info.Models) > 0 {
		model = info.Models[0]
	}

	if id == "anthropic" {
		if apiKey == "" {
			return nil, fmt.Errorf("provider=anthropic requires an Anthropic API key")
		}
		c := newAnthropicClient(apiKey, model, cfg.LLMMaxTokens, sampling)
		c.planToks = cfg.LLMPlanMaxTokens
		return c, nil
	}

	if baseURL == "" {
		return nil, fmt.Errorf("provider=%s requires an API URL", id)
	}
	c := newOpenAIClient(baseURL, apiKey, model, id, cfg.LLMMaxTokens, sampling)
	c.planToks = cfg.LLMPlanMaxTokens
	return c, nil
}
