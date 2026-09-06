package web

import (
	"encoding/json"
	"net/http"
	"time"

	"goeat/llm"
)

type settingsPageData struct {
	HasLLM       bool
	Provider     string
	Model        string
	WeekStartDay string
	ImageDir     string
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	model := s.cfg.LLMModel
	if model == "" {
		model = s.cfg.AnthropicModel
	}

	s.render(w, r, "settings", settingsPageData{
		HasLLM:       s.gen != nil,
		Provider:     s.cfg.Provider,
		Model:        model,
		WeekStartDay: s.cfg.WeekStartDay,
		ImageDir:     s.cfg.RecipeImageDir,
	})
}

// handleSettingsTestAI sends a trivial prompt to the configured LLM and returns
// JSON so the settings page can display the result inline.
//
//	POST /settings/test-ai
//	→ {"ok":true,"prompt":"…","response":"…","duration_ms":1234,"model":"…"}
//	→ {"ok":false,"prompt":"…","error":"…","duration_ms":1234}
func (s *Server) handleSettingsTestAI(w http.ResponseWriter, r *http.Request) {
	type result struct {
		OK         bool   `json:"ok"`
		Prompt     string `json:"prompt"`
		Response   string `json:"response,omitempty"`
		Error      string `json:"error,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		Model      string `json:"model,omitempty"`
	}

	w.Header().Set("Content-Type", "application/json")

	testPrompt := `Reply with exactly one sentence: "Go Eat AI is working!"`

	if s.gen == nil {
		json.NewEncoder(w).Encode(result{OK: false, Prompt: testPrompt, Error: "No LLM provider configured"})
		return
	}

	start := time.Now()
	resp, err := s.gen.Generate(r.Context(), llm.GenerateRequest{
		System: "You are a helpful assistant.",
		Prompt: testPrompt,
	})
	ms := time.Since(start).Milliseconds()

	if err != nil {
		json.NewEncoder(w).Encode(result{OK: false, Prompt: testPrompt, Error: err.Error(), DurationMS: ms})
		return
	}

	json.NewEncoder(w).Encode(result{
		OK:         true,
		Prompt:     testPrompt,
		Response:   resp.Content,
		DurationMS: ms,
		Model:      resp.ModelName,
	})
}

// handleLLMDebugLog returns the last N captured LLM calls as JSON.
//
//	GET /admin/llm-debug
func (s *Server) handleLLMDebugLog(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		At         string `json:"at"`
		System     string `json:"system"`
		Prompt     string `json:"prompt"`
		Response   string `json:"response,omitempty"`
		Error      string `json:"error,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		InputToks  int    `json:"input_tokens,omitempty"`
		OutputToks int    `json:"output_tokens,omitempty"`
		Model      string `json:"model,omitempty"`
	}

	w.Header().Set("Content-Type", "application/json")

	if llm.GlobalDebugLog == nil {
		json.NewEncoder(w).Encode([]entry{})
		return
	}

	raw := llm.GlobalDebugLog.Entries()
	// Reverse so newest is first.
	out := make([]entry, len(raw))
	for i, e := range raw {
		out[len(raw)-1-i] = entry{
			At:         e.At.Format(time.RFC3339),
			System:     e.System,
			Prompt:     e.Prompt,
			Response:   e.Response,
			Error:      e.Error,
			DurationMS: e.DurationMS,
			InputToks:  e.InputToks,
			OutputToks: e.OutputToks,
			Model:      e.Model,
		}
	}

	json.NewEncoder(w).Encode(out)
}
