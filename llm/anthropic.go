package llm

import (
	"context"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicClient uses the native Anthropic Go SDK (§7.2, §16.4).
// Phase 2 uses it for free-text parsing; Phase 3 adds structured outputs
// and adaptive thinking for meal-plan generation.
type anthropicClient struct {
	client   anthropic.Client
	model    string
	maxToks  int
	sampling Sampling
}

func newAnthropicClient(apiKey, model string, maxToks int, s Sampling) *anthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	return &anthropicClient{
		client:   anthropic.NewClient(opts...),
		model:    model,
		maxToks:  maxToks,
		sampling: s,
	}
}

func (c *anthropicClient) ProviderName() string { return "anthropic" }
func (c *anthropicClient) ModelName() string    { return c.model }

func (c *anthropicClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	maxToks := req.MaxTokens
	if maxToks <= 0 {
		maxToks = c.maxToks
	}

	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: int64(maxToks),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt)),
		},
	}
	// Anthropic accepts temperature/top_p/top_k but has no min_p or
	// presence_penalty; those two are silently skipped here.
	if c.sampling.Temperature > 0 {
		params.Temperature = anthropic.Float(clampTemp(c.sampling.Temperature))
	}
	if c.sampling.TopP > 0 {
		params.TopP = anthropic.Float(c.sampling.TopP)
	}
	if c.sampling.TopK > 0 {
		params.TopK = anthropic.Int(int64(c.sampling.TopK))
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	msg, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("anthropic: %w", err)
	}

	var text string
	for _, block := range msg.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}

	return GenerateResponse{
		Content:      text,
		InputTokens:  int(msg.Usage.InputTokens),
		OutputTokens: int(msg.Usage.OutputTokens),
		ProviderName: "anthropic",
		ModelName:    msg.Model,
	}, nil
}

// clampTemp keeps temperature inside the 0-1 range the Anthropic API accepts;
// the shared default (1.01) is tuned for local models that allow more.
func clampTemp(t float64) float64 {
	if t > 1 {
		return 1
	}
	if t < 0 {
		return 0
	}
	return t
}
