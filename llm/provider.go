package llm

import (
	"fmt"

	"goeat/config"
)

// NewGenerator returns a Generator for the configured provider profile.
// Returns an error if the provider requires credentials that are not set.
func NewGenerator(cfg *config.Config) (Generator, error) {
	switch cfg.Provider {
	case "anthropic":
		if cfg.AnthropicAPIKey == "" {
			return nil, fmt.Errorf("provider=anthropic requires ANTHROPIC_API_KEY")
		}
		return newAnthropicClient(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.LLMMaxTokens), nil

	case "openai":
		if cfg.LLMAPIUrl == "" {
			return newOpenAIClient("https://api.openai.com", cfg.LLMAPIKey,
				cfg.LLMModel, "openai", cfg.LLMMaxTokens, cfg.LLMTemp), nil
		}
		return newOpenAIClient(cfg.LLMAPIUrl, cfg.LLMAPIKey,
			cfg.LLMModel, "openai", cfg.LLMMaxTokens, cfg.LLMTemp), nil

	case "google":
		baseURL := cfg.LLMAPIUrl
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		return newOpenAIClient(baseURL, cfg.LLMAPIKey,
			cfg.LLMModel, "google", cfg.LLMMaxTokens, cfg.LLMTemp), nil

	case "openai_compatible", "":
		if cfg.LLMAPIUrl == "" {
			return nil, fmt.Errorf("provider=openai_compatible requires LLM_API_URL")
		}
		return newOpenAIClient(cfg.LLMAPIUrl, cfg.LLMAPIKey,
			cfg.LLMModel, "openai_compatible", cfg.LLMMaxTokens, cfg.LLMTemp), nil

	default:
		return nil, fmt.Errorf("unknown LLM provider %q (valid: anthropic, openai, google, openai_compatible)", cfg.Provider)
	}
}
