// Package llm provides a provider-agnostic LLM client (§7.1, §7.2).
// The Generator interface abstracts Anthropic's native SDK and any
// OpenAI-compatible endpoint behind a single Generate call. Phase 2 uses
// it for free-text meal-description parsing; Phase 3 adds structured
// meal-plan generation.
package llm

import (
	"context"
	"errors"
)

// ErrTruncated is wrapped by a Generate error when the call hit its output
// limit before producing any usable content (e.g. a reasoning model that
// spent the whole budget thinking). A cut-off call that did produce content
// returns no error and sets GenerateResponse.Truncated instead.
var ErrTruncated = errors.New("output token limit reached")

// GenerateRequest is the input to Generator.Generate.
type GenerateRequest struct {
	System    string // system prompt
	Prompt    string // user turn
	MaxTokens int    // 0 → use provider default

	// SuppressReasoning asks a reasoning-capable model to answer directly
	// instead of thinking out loud first. Set it on short, strictly-formatted
	// calls (a price estimate, a selector guess) where the chain of thought is
	// worthless and actively harmful: a thinking model spends the whole token
	// budget on reasoning and returns empty content, which reads downstream as
	// "unexpected end of JSON input". Leave it off for the big generation
	// calls, which have the headroom and benefit from the reasoning.
	//
	// Honoured only by backends that expose the switch; on the rest it is a
	// no-op and the larger token budget is what saves the call.
	SuppressReasoning bool

	// OnDelta, when set, asks the backend to stream the reply and invokes this
	// callback with each incremental chunk of text as it arrives, in addition
	// to returning the assembled GenerateResponse as usual once the call
	// finishes. nil (the default) makes an ordinary blocking call - callers
	// that have nowhere to show a live reply (pricing estimates, the free-text
	// parser) leave it unset. Called from whatever goroutine is doing the
	// generation; it must not block or it stalls the stream.
	OnDelta func(chunk string)
}

// GenerateResponse is the output from Generator.Generate.
type GenerateResponse struct {
	Content      string
	InputTokens  int
	OutputTokens int
	ProviderName string
	ModelName    string

	// Truncated is true when the provider stopped because it hit MaxTokens
	// (Anthropic stop_reason "max_tokens", OpenAI finish_reason "length")
	// rather than finishing its answer. Content is then whatever was produced
	// before the cut - for a JSON reply, almost certainly unparseable.
	Truncated bool

	// RunID is the ai_runs row the recorder (see NewRunRecorder) wrote for
	// this call, or 0 when the call was not recorded.
	RunID int64
}

// DefaultPlanMaxTokens is the plan-generation output budget when nothing
// else is configured. Bumped from 8192 after real responses were getting cut
// off mid-JSON on busy weeks.
const DefaultPlanMaxTokens = 16384

// planLimiter is implemented by every Generator that knows its configured
// plan-generation budget (LLM_PLAN_MAX_TOKENS). Wrappers forward it.
type planLimiter interface {
	PlanMaxTokens() int
}

// PlanMaxTokens returns the output budget for one plan-generation call:
// the configured LLM_PLAN_MAX_TOKENS when gen exposes one, otherwise
// DefaultPlanMaxTokens.
func PlanMaxTokens(gen Generator) int {
	if l, ok := gen.(planLimiter); ok {
		if n := l.PlanMaxTokens(); n > 0 {
			return n
		}
	}
	return DefaultPlanMaxTokens
}

// Generator is the single interface all LLM backends implement.
// Callers never import provider-specific packages; they receive a Generator
// from NewGenerator and call Generate.
type Generator interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	ProviderName() string
	ModelName() string
}
