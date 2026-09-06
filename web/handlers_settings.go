package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
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

// handleLLMDebugLog returns the 10 most recent LLM calls as JSON (used by the
// settings page inline preview). The full log is at GET /admin/llm-log.
//
//	GET /admin/llm-debug
func (s *Server) handleLLMDebugLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	entries := llmDebugEntries()
	if len(entries) > 10 {
		entries = entries[:10]
	}
	json.NewEncoder(w).Encode(entries)
}

// handleLLMLogPage renders the full paginated AI call log with search + filters.
//
//	GET /admin/llm-log
func (s *Server) handleLLMLogPage(w http.ResponseWriter, r *http.Request) {
	all := llmDebugEntries()

	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status") // "ok" | "error" | ""

	// Filter in-memory (ring buffer is small — at most 30 entries)
	filtered := all[:0:len(all)]
	for _, e := range all {
		if status == "ok" && e.Error != "" {
			continue
		}
		if status == "error" && e.Error == "" {
			continue
		}
		if q != "" {
			qLow := strings.ToLower(q)
			if !strings.Contains(strings.ToLower(e.Prompt), qLow) &&
				!strings.Contains(strings.ToLower(e.Response), qLow) &&
				!strings.Contains(strings.ToLower(e.System), qLow) {
				continue
			}
		}
		filtered = append(filtered, e)
	}

	const perPage = 10
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = p
	}
	total := len(filtered)
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * perPage
	end := start + perPage
	if end > total {
		end = total
	}
	pageEntries := filtered[start:end]

	prevPage := page - 1
	nextPage := 0
	if page < totalPages {
		nextPage = page + 1
	}

	type llmLogPageData struct {
		Entries    []llmDebugEntry
		Query      string
		Status     string
		Page       int
		PrevPage   int
		NextPage   int
		TotalPages int
		Total      int
	}

	s.render(w, r, "llm_log", llmLogPageData{
		Entries:    pageEntries,
		Query:      q,
		Status:     status,
		Page:       page,
		PrevPage:   prevPage,
		NextPage:   nextPage,
		TotalPages: totalPages,
		Total:      total,
	})
}

type llmDebugEntry struct {
	At         string
	System     string
	Prompt     string
	Response   string
	Error      string
	DurationMS int64
	InputToks  int
	OutputToks int
	Model      string
}

func llmDebugEntries() []llmDebugEntry {
	if llm.GlobalDebugLog == nil {
		return nil
	}
	raw := llm.GlobalDebugLog.Entries()
	out := make([]llmDebugEntry, len(raw))
	for i, e := range raw {
		// Reverse so newest is first
		out[len(raw)-1-i] = llmDebugEntry{
			At:         e.At.Format("2006-01-02 15:04:05"),
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
	return out
}
