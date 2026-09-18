package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
		// 15 min - a backstop, not the real budget: web/handlers_plan.go's
		// generation job already bounds every call at 12 min via ctx, and a
		// client.Timeout tighter than that fires first regardless of how much
		// of the job's own budget is left, which is exactly what was cutting
		// off slow local models mid-stream. This only needs to be looser than
		// that ceiling, not tight itself.
		httpCli: &http.Client{Timeout: 15 * time.Minute},
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
	// presence_penalty is a standard OpenAI field, but at least some Gemini
	// models 400 on any nonzero value ("Penalty is not enabled for this
	// model") rather than ignoring it - and there is no reliable way to know
	// per-model which ones do. Sent to openai and openai_compatible only.
	if c.sampling.Presence != 0 && c.provider != "google" {
		body.PresencePenalty = &c.sampling.Presence
	}
	if req.OnDelta != nil {
		body.Stream = true
		// Without this the final chunk before [DONE] carries no usage figures
		// at all under the OpenAI streaming wire format, and callers (cost
		// estimates, the debug log) would silently lose input/output token
		// counts for every streamed call. Gemini's compatibility layer 400s on
		// unrecognised fields, matching presence_penalty's gating above.
		if c.provider != "google" {
			body.StreamOptions = &streamOptions{IncludeUsage: true}
		}
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
		// Qwen3-family chat templates (llama.cpp, vLLM, LM Studio, Ollama)
		// take enable_thinking through chat_template_kwargs. Sent only for a
		// generic OpenAI-compatible endpoint, and only when the caller asked
		// for a direct answer - api.openai.com and the Gemini compatibility
		// layer reject unknown fields.
		if req.SuppressReasoning {
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
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
	if body.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	var cr chatResp
	if body.Stream {
		cr, err = c.doStreamRequest(httpReq, req.OnDelta)
	} else {
		cr, err = c.doRequest(httpReq)
	}
	if err != nil {
		return GenerateResponse{}, err
	}
	if len(cr.Choices) == 0 {
		return GenerateResponse{}, fmt.Errorf("openai: no choices in response")
	}

	choice := cr.Choices[0]
	content := stripThinkBlocks(choice.Message.Content)

	// A reasoning model that runs out of budget mid-thought answers with empty
	// content and its whole output in reasoning_content. Callers then fail to
	// parse "" and report "unexpected end of JSON input", which says nothing
	// about the actual cause. Name it instead.
	if strings.TrimSpace(content) == "" {
		reasoned := strings.TrimSpace(choice.Message.ReasoningContent)
		switch {
		case choice.FinishReason == "length" && reasoned != "":
			return GenerateResponse{}, fmt.Errorf(
				"openai: model spent all %d output tokens reasoning and returned no answer - raise max_tokens or disable thinking for this call",
				cr.Usage.CompletionTokens)
		case choice.FinishReason == "length":
			return GenerateResponse{}, fmt.Errorf(
				"openai: response was cut off at the %d-token limit before any content was produced",
				cr.Usage.CompletionTokens)
		case reasoned != "":
			return GenerateResponse{}, fmt.Errorf(
				"openai: model returned only reasoning, no answer (finish_reason %q)", choice.FinishReason)
		default:
			return GenerateResponse{}, fmt.Errorf(
				"openai: model returned empty content (finish_reason %q)", choice.FinishReason)
		}
	}

	return GenerateResponse{
		Content:      content,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
		ProviderName: c.provider,
		ModelName:    cr.Model,
	}, nil
}

// doRequest is the ordinary blocking call: send, read the whole body, decode
// one JSON object.
func (c *openAIClient) doRequest(httpReq *http.Request) (chatResp, error) {
	resp, err := c.httpCli.Do(httpReq)
	if err != nil {
		return chatResp{}, fmt.Errorf("openai: do: %w", err)
	}
	defer resp.Body.Close()

	respRaw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return chatResp{}, fmt.Errorf("openai: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return chatResp{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, respRaw)
	}

	var cr chatResp
	if err := json.Unmarshal(respRaw, &cr); err != nil {
		return chatResp{}, fmt.Errorf("openai: decode: %w", err)
	}
	return cr, nil
}

// doStreamRequest sends and reads a streamed chat completion, invoking
// onDelta (when set) with each chunk of assistant text as it arrives and
// assembling the same chatResp shape doRequest returns, so the caller's
// post-processing (empty-content classification, usage, model name) is
// identical either way.
func (c *openAIClient) doStreamRequest(httpReq *http.Request, onDelta func(string)) (chatResp, error) {
	resp, err := c.httpCli.Do(httpReq)
	if err != nil {
		return chatResp{}, fmt.Errorf("openai: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return chatResp{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, respRaw)
	}

	var cr chatResp
	var content, reasoning strings.Builder
	var finishReason string

	scanner := bufio.NewScanner(resp.Body)
	// A single SSE line can carry a large tool-call payload or a long
	// reasoning chunk; the default 64KiB scanner buffer truncates those
	// (bufio.ErrTooLong) well before this app's own request bodies would.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // blank frame separators and any other SSE field are irrelevant here
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // a frame this client doesn't understand; skip rather than fail the stream
		}
		if chunk.Model != "" {
			cr.Model = chunk.Model
		}
		if chunk.Usage != nil {
			cr.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0]
		if delta.FinishReason != "" {
			finishReason = delta.FinishReason
		}
		if delta.Delta.Content != "" {
			content.WriteString(delta.Delta.Content)
			if onDelta != nil {
				onDelta(delta.Delta.Content)
			}
		}
		if delta.Delta.ReasoningContent != "" {
			reasoning.WriteString(delta.Delta.ReasoningContent)
		}
	}
	if err := scanner.Err(); err != nil {
		return chatResp{}, fmt.Errorf("openai: read stream: %w", err)
	}

	cr.Choices = []chatChoice{{
		Message:      chatMsg{Role: "assistant", Content: content.String(), ReasoningContent: reasoning.String()},
		FinishReason: finishReason,
	}}
	return cr, nil
}

// ── wire types ──────────────────────────────────────────────────────────────

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningContent is where OpenAI-compatible servers put a reasoning
	// model's chain of thought, separate from the answer. Never sent on a
	// request; read only to explain an empty Content.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatReq struct {
	Model           string         `json:"model"`
	Messages        []chatMsg      `json:"messages"`
	MaxTokens       int            `json:"max_tokens"`
	Temperature     float64        `json:"temperature"`
	TopP            *float64       `json:"top_p,omitempty"`
	PresencePenalty *float64       `json:"presence_penalty,omitempty"`
	TopK            *int           `json:"top_k,omitempty"`
	MinP            *float64       `json:"min_p,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`

	// ChatTemplateKwargs passes arguments into the server's chat template -
	// the standard way to toggle Qwen-style thinking.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

// streamOptions.IncludeUsage asks for a final streamed chunk that carries the
// same usage totals a non-streamed response gets for free - without it the
// wire format has nowhere to put them.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatChoice struct {
	Message      chatMsg `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResp struct {
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

// chatStreamChunk is one `data:` frame of a streamed chat completion - the
// incremental sibling of chatResp. Only the delta and finish_reason are
// per-chunk; Model and Usage repeat (or, for Usage, appear once at the end)
// and are folded into the accumulated chatResp by the caller.
type chatStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta        chatMsg `json:"delta"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// stripThinkBlocks removes inline <think>…</think> reasoning. Servers that
// separate reasoning into reasoning_content leave nothing to strip; the ones
// that inline it into the answer would otherwise hand every caller a blob of
// prose wrapped around the JSON they asked for. An unterminated block means
// the reply was cut off mid-thought, so nothing usable follows it either.
func stripThinkBlocks(s string) string {
	for {
		open := strings.Index(s, "<think>")
		if open < 0 {
			return strings.TrimSpace(s)
		}
		close := strings.Index(s[open:], "</think>")
		if close < 0 {
			return strings.TrimSpace(s[:open])
		}
		s = s[:open] + s[open+close+len("</think>"):]
	}
}
