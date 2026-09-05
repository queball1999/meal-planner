// Package llm provides a provider-agnostic LLM client (§7.1, §7.2).
// The Generator interface abstracts Anthropic's native SDK and any
// OpenAI-compatible endpoint behind a single Generate call. Phase 2 uses
// it for free-text meal-description parsing; Phase 3 adds structured
// meal-plan generation.
package llm

import "context"

// GenerateRequest is the input to Generator.Generate.
type GenerateRequest struct {
	System    string // system prompt
	Prompt    string // user turn
	MaxTokens int    // 0 → use provider default
}

// GenerateResponse is the output from Generator.Generate.
type GenerateResponse struct {
	Content          string
	InputTokens      int
	OutputTokens     int
	ProviderName     string
	ModelName        string
}

// Generator is the single interface all LLM backends implement.
// Callers never import provider-specific packages; they receive a Generator
// from NewGenerator and call Generate.
type Generator interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	ProviderName() string
	ModelName() string
}
