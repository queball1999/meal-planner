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
	client  anthropic.Client
	model   string
	maxToks int
}

func newAnthropicClient(apiKey, model string, maxToks int) *anthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	return &anthropicClient{
		client:  anthropic.NewClient(opts...),
		model:   model,
		maxToks: maxToks,
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
