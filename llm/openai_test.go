package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points an openai_compatible client at a stub server.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*openAIClient, *[]byte) {
	t.Helper()
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return newOpenAIClient(srv.URL, "", "test-model", "openai_compatible", 1024, Sampling{}), &lastBody
}

func respondJSON(t *testing.T, w http.ResponseWriter, payload string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, payload)
}

// A reasoning model that exhausts its budget thinking answers with empty
// content. The old code passed that "" straight to the caller, which reported
// "unexpected end of JSON input" - true, but useless. It must name the cause.
func TestGenerate_EmptyContentAfterReasoningIsADescriptiveError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(t, w, `{
			"model":"test-model",
			"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"","reasoning_content":"Let me think about eggs..."}}],
			"usage":{"prompt_tokens":96,"completion_tokens":128}
		}`)
	})

	_, err := c.Generate(context.Background(), GenerateRequest{Prompt: "price of eggs"})
	if err == nil {
		t.Fatal("want an error for empty content, got nil")
	}
	if !strings.Contains(err.Error(), "reasoning") {
		t.Errorf("error should name reasoning as the cause, got: %v", err)
	}
	if !strings.Contains(err.Error(), "128") {
		t.Errorf("error should report the token budget it burned, got: %v", err)
	}
}

func TestGenerate_EmptyContentWithoutReasoningStillErrors(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(t, w, `{
			"model":"test-model",
			"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"  "}}],
			"usage":{"prompt_tokens":10,"completion_tokens":0}
		}`)
	})

	if _, err := c.Generate(context.Background(), GenerateRequest{Prompt: "x"}); err == nil {
		t.Fatal("want an error for blank content, got nil")
	}
}

// SuppressReasoning must reach the server as the chat-template switch, and
// must not be sent when the caller didn't ask for it.
func TestGenerate_SuppressReasoningSendsChatTemplateKwargs(t *testing.T) {
	ok := `{"model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}],"usage":{}}`

	c, body := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { respondJSON(t, w, ok) })

	if _, err := c.Generate(context.Background(), GenerateRequest{Prompt: "x", SuppressReasoning: true}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	kwargs, present := sent["chat_template_kwargs"].(map[string]any)
	if !present {
		t.Fatalf("chat_template_kwargs missing from request: %s", *body)
	}
	if kwargs["enable_thinking"] != false {
		t.Errorf("enable_thinking = %v, want false", kwargs["enable_thinking"])
	}

	if _, err := c.Generate(context.Background(), GenerateRequest{Prompt: "x"}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	sent = nil // Unmarshal merges into a non-nil map; start clean
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, present := sent["chat_template_kwargs"]; present {
		t.Errorf("chat_template_kwargs should be absent when reasoning is allowed: %s", *body)
	}
}

// api.openai.com rejects unknown fields, so the switch is only for generic
// OpenAI-compatible backends.
func TestGenerate_SuppressReasoningNotSentToOpenAI(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		respondJSON(t, w, `{"model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}],"usage":{}}`)
	}))
	t.Cleanup(srv.Close)

	c := newOpenAIClient(srv.URL, "", "gpt-x", "openai", 1024, Sampling{})
	if _, err := c.Generate(context.Background(), GenerateRequest{Prompt: "x", SuppressReasoning: true}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if strings.Contains(string(lastBody), "chat_template_kwargs") {
		t.Errorf("chat_template_kwargs must not be sent to the openai provider: %s", lastBody)
	}
}

func TestStripThinkBlocks(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":1}`, `{"a":1}`},
		{"<think>hmm</think>\n{\"a\":1}", `{"a":1}`},
		{"before<think>hmm</think>after", "beforeafter"},
		{"<think>never closed, reply was cut off", ""},
		{"<think>one</think>x<think>two</think>y", "xy"},
	}
	for _, c := range cases {
		if got := stripThinkBlocks(c.in); got != c.want {
			t.Errorf("stripThinkBlocks(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An inlined chain of thought must not reach the caller as part of the answer.
func TestGenerate_StripsInlineThinkBlock(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(t, w, `{
			"model":"m",
			"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"<think>eggs are about $4</think>{\"price_cents\":450}"}}],
			"usage":{}
		}`)
	})

	resp, err := c.Generate(context.Background(), GenerateRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if resp.Content != `{"price_cents":450}` {
		t.Errorf("Content = %q, want the JSON alone", resp.Content)
	}
}

// A streamed call hands reasoning and answer to their own callbacks, under
// either field name a server uses for the chain of thought, and reasoning
// never leaks into Content.
func TestGenerate_StreamRoutesReasoningAndContentSeparately(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"model":"m","choices":[{"delta":{"reasoning_content":"Let me "}}]}`,
			`data: {"model":"m","choices":[{"delta":{"reasoning":"think."}}]}`,
			`data: {"model":"m","choices":[{"delta":{"content":"{\"ok\":"}}]}`,
			`data: {"model":"m","choices":[{"delta":{"content":"true}"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}, "\n\n")+"\n\n")
	})

	var reasoning, content strings.Builder
	resp, err := c.Generate(context.Background(), GenerateRequest{
		Prompt:      "x",
		OnDelta:     func(s string) { content.WriteString(s) },
		OnReasoning: func(s string) { reasoning.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := reasoning.String(); got != "Let me think." {
		t.Errorf("reasoning = %q, want %q", got, "Let me think.")
	}
	if got := content.String(); got != `{"ok":true}` {
		t.Errorf("streamed content = %q", got)
	}
	if resp.Content != `{"ok":true}` {
		t.Errorf("Content = %q, reasoning must not leak into it", resp.Content)
	}
}
