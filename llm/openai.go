package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// openAIClient speaks the OpenAI Chat Completions wire format.
// Covers the openai, google, and openai_compatible provider profiles (§7.2).
type openAIClient struct {
	baseURL  string
	apiKey   string
	model    string
	maxToks  int
	sampling Sampling
	provider string
	httpCli  *http.Client
}

// Sampling carries the decoding parameters shared by every provider.
// TopK and MinP are not part of the OpenAI API and are only sent to
// openai_compatible backends, which is where they are understood.
type Sampling struct {
	Temperature float64
	TopP        float64
	TopK        int
	MinP        float64
	Presence    float64
}

func newOpenAIClient(baseURL, apiKey, model, provider string, maxToks int, s Sampling) *openAIClient {
	return &openAIClient{
		baseURL:  baseURL,
		apiKey:   apiKey,
		model:    model,
		maxToks:  maxToks,
		sampling: s,
		provider: provider,
		httpCli:  &http.Client{Timeout: 300 * time.Second}, // 5 min - large local models are slow
	}
}

func (c *openAIClient) ProviderName() string { return c.provider }
func (c *openAIClient) ModelName() string    { return c.model }

func (c *openAIClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	maxToks := req.MaxTokens
	if maxToks <= 0 {
		maxToks = c.maxToks
	}

	msgs := []chatMsg{}
	if req.System != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: req.System})
	}
	msgs = append(msgs, chatMsg{Role: "user", Content: req.Prompt})

	body := chatReq{
		Model:       c.model,
		Messages:    msgs,
		MaxTokens:   maxToks,
		Temperature: c.sampling.Temperature,
	}
	if c.sampling.TopP > 0 {
		body.TopP = &c.sampling.TopP
	}
	if c.sampling.Presence != 0 {
		body.PresencePenalty = &c.sampling.Presence
	}
	// top_k / min_p are llama.cpp-family extensions; api.openai.com and the
	// Gemini compatibility layer reject unknown fields, so send them only to
	// a generic OpenAI-compatible endpoint.
	if c.provider == "openai_compatible" {
		if c.sampling.TopK > 0 {
			body.TopK = &c.sampling.TopK
		}
		if c.sampling.MinP > 0 {
			body.MinP = &c.sampling.MinP
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("openai: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		APIBase(c.baseURL, c.provider)+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpCli.Do(httpReq)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("openai: do: %w", err)
	}
	defer resp.Body.Close()

	respRaw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("openai: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return GenerateResponse{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, respRaw)
	}

	var cr chatResp
	if err := json.Unmarshal(respRaw, &cr); err != nil {
		return GenerateResponse{}, fmt.Errorf("openai: decode: %w", err)
	}
	if len(cr.Choices) == 0 {
		return GenerateResponse{}, fmt.Errorf("openai: no choices in response")
	}

	return GenerateResponse{
		Content:      cr.Choices[0].Message.Content,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
		ProviderName: c.provider,
		ModelName:    cr.Model,
	}, nil
}

// ── wire types ──────────────────────────────────────────────────────────────

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model           string    `json:"model"`
	Messages        []chatMsg `json:"messages"`
	MaxTokens       int       `json:"max_tokens"`
	Temperature     float64   `json:"temperature"`
	TopP            *float64  `json:"top_p,omitempty"`
	PresencePenalty *float64  `json:"presence_penalty,omitempty"`
	TopK            *int      `json:"top_k,omitempty"`
	MinP            *float64  `json:"min_p,omitempty"`
}

type chatResp struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
