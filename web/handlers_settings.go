package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"goeat/db"
	"goeat/llm"
	"goeat/scrape"
	"goeat/settings"
)

// settingsFieldView is one editable row on the Settings page.
type settingsFieldView struct {
	Key     string
	Role    string // AI fields only: "key" | "model" | "url"
	Label   string
	Help    string
	Kind    string // "string" | "int" | "float" | "select" | "secret"
	Options []string
	Value   string // "" for a secret - never sent to the browser
	IsSet   bool   // secrets only: whether a value exists server-side
	Source  string // "env" | "admin"
}

type settingsCategoryView struct {
	Name   string
	Fields []settingsFieldView
}

// aiProviderView is one entry in the provider picker plus the credential
// fields for that provider. Every provider is rendered; the page shows one
// panel at a time, so switching the picker never discards another provider's
// stored values.
type aiProviderView struct {
	ID         string
	Label      string
	DocsURL    string
	DefaultURL string
	ShowURL    bool
	Configured bool // has enough saved to be usable
	IsDefault  bool // currently selected as PROVIDER
	Models     []string
	KeyField   *settingsFieldView
	ModelField *settingsFieldView
	URLField   *settingsFieldView
}

type settingsPageData struct {
	HasLLM       bool
	ActiveID     string
	Providers    []aiProviderView
	SharedFields []settingsFieldView // sampling params, applied to every provider
	Categories   []settingsCategoryView
}

var kindNames = map[settings.Kind]string{
	settings.KindString: "string",
	settings.KindInt:    "int",
	settings.KindFloat:  "float",
	settings.KindSelect: "select",
	settings.KindSecret: "secret",
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSettings(r.Context())
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	byKey := make(map[string]*db.Setting, len(rows))
	for _, row := range rows {
		byKey[row.Key] = row
	}
	stored := func(key string) string {
		if row := byKey[key]; row != nil {
			return row.Value
		}
		return ""
	}

	// AI fields are pulled out of the ordinary category loop: they are
	// rendered by the provider panel instead, keyed by Definition.Provider.
	aiByProvider := make(map[string][]*settingsFieldView)
	var sharedAI []settingsFieldView

	var categories []settingsCategoryView
	catIndex := make(map[string]int)

	for _, d := range settings.Defs {
		row := byKey[d.Key]

		field := settingsFieldView{
			Key:     d.Key,
			Role:    d.Role,
			Label:   d.Label,
			Help:    d.Help,
			Kind:    kindNames[d.Kind],
			Options: d.Options,
		}
		if row != nil {
			field.Source = row.Source
			if d.Kind == settings.KindSecret {
				field.IsSet = row.Value != ""
			} else {
				field.Value = row.Value
			}
		}

		if d.Category == "AI Provider" {
			switch {
			case d.Provider != "":
				f := field
				aiByProvider[d.Provider] = append(aiByProvider[d.Provider], &f)
			case d.Key != "PROVIDER":
				sharedAI = append(sharedAI, field)
			}
			continue
		}

		i, ok := catIndex[d.Category]
		if !ok {
			i = len(categories)
			catIndex[d.Category] = i
			categories = append(categories, settingsCategoryView{Name: d.Category})
		}
		categories[i].Fields = append(categories[i].Fields, field)
	}

	active := stored("PROVIDER")
	if _, ok := llm.ProviderByID(active); !ok {
		active = s.cfg.Provider
	}

	providers := make([]aiProviderView, 0, len(llm.Providers))
	for _, info := range llm.Providers {
		pv := aiProviderView{
			ID:         info.ID,
			Label:      info.Label,
			DocsURL:    info.DocsURL,
			DefaultURL: info.DefaultURL,
			ShowURL:    info.ShowURL,
			IsDefault:  info.ID == active,
		}
		for _, f := range aiByProvider[info.ID] {
			switch f.Role {
			case "key":
				pv.KeyField = f
			case "model":
				pv.ModelField = f
			case "url":
				pv.URLField = f
			}
		}
		// Configured reflects what is actually saved, so it must be read
		// before the URL field is prefilled with the catalog default below.
		pv.Configured = providerConfigured(info, pv, stored)
		// Prefill an unset URL with the provider's default so the field is
		// never blank and "Refresh list" has somewhere to go.
		if pv.URLField != nil && pv.URLField.Value == "" {
			pv.URLField.Value = info.DefaultURL
		}
		pv.Models = modelChoices(info, pv.ModelField)
		providers = append(providers, pv)
	}

	s.render(w, r, "settings", settingsPageData{
		HasLLM:       s.gen != nil,
		ActiveID:     active,
		Providers:    providers,
		SharedFields: sharedAI,
		Categories:   categories,
	})
}

// modelChoices is the curated model list for a provider with the currently
// saved model folded in, so a hand-typed model still shows as selected.
func modelChoices(info llm.ProviderInfo, modelField *settingsFieldView) []string {
	out := append([]string(nil), info.Models...)
	if modelField == nil || modelField.Value == "" {
		return out
	}
	for _, m := range out {
		if m == modelField.Value {
			return out
		}
	}
	return append([]string{modelField.Value}, out...)
}

// providerConfigured reports whether a provider has enough saved to be
// selectable as the default: a key, or - for a local OpenAI-compatible
// server, which usually needs none - a URL.
func providerConfigured(info llm.ProviderInfo, pv aiProviderView, stored func(string) string) bool {
	if info.ID == "openai_compatible" {
		return pv.URLField != nil && pv.URLField.Value != ""
	}
	return pv.KeyField != nil && stored(pv.KeyField.Key) != ""
}

// handleAIModels asks a provider which models the saved key can use, so the
// model dropdown reflects the account rather than a hard-coded list. On any
// failure it falls back to the curated catalog and reports why.
//
//	POST /settings/ai/models  {"provider":"openai"}
//	→ {"ok":true,"models":[...],"source":"live"|"catalog","error":"…"}
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	info, ok := llm.ProviderByID(body.Provider)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown provider"})
		return
	}

	// Read the saved credentials rather than anything posted: the page
	// autosaves on blur, and a secret is never sent back to the browser.
	cfg := *s.cfg
	if err := settings.Apply(r.Context(), s.store, &cfg, func(string, ...any) {}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "failed to read settings"})
		return
	}
	apiKey, baseURL, _ := llm.ProviderCreds(&cfg, info.ID)

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	models, err := llm.ListModels(ctx, info.ID, apiKey, baseURL)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "models": info.Models, "source": "catalog", "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": models, "source": "live"})
}

// handleSettingsSave persists one edited setting.
//
//	POST /settings/save  {"key":"...","value":"..."}
//	→ {"ok":true}
//	→ {"ok":false,"error":"…"}
//
// A blank value for a KindSecret field is a no-op (keeps the existing
// value) rather than clearing it - the browser never receives a secret's
// current value, so an untouched field always resubmits as blank.
func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	def, ok := settings.ByKey(body.Key)
	if !ok {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "unknown setting"})
		return
	}

	if def.Kind == settings.KindSecret && body.Value == "" {
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}

	if err := settings.Validate(def, body.Value); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	if err := s.store.SetSetting(r.Context(), def.Key, body.Value); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "save failed"})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true})
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
		Provider   string `json:"provider,omitempty"`
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
		json.NewEncoder(w).Encode(result{OK: false, Prompt: testPrompt, Error: err.Error(), DurationMS: ms, Provider: s.gen.ProviderName()})
		return
	}

	json.NewEncoder(w).Encode(result{
		OK:         true,
		Prompt:     testPrompt,
		Response:   resp.Content,
		DurationMS: ms,
		Provider:   s.gen.ProviderName(),
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

	// Filter in-memory (ring buffer is small - at most 30 entries)
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

	pageEntries, page := paginate(r, filtered)

	type llmLogPageData struct {
		Entries []llmDebugEntry
		Query   string
		Status  string
		Page    Pagination
	}

	s.render(w, r, "llm_log", llmLogPageData{
		Entries: pageEntries,
		Query:   q,
		Status:  status,
		Page:    page,
	})
}

type llmDebugEntry struct {
	At         string `json:"at"`
	System     string `json:"system,omitempty"`
	Prompt     string `json:"prompt"`
	Response   string `json:"response,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	InputToks  int    `json:"input_tokens,omitempty"`
	OutputToks int    `json:"output_tokens,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
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
			Provider:   e.Provider,
			Model:      e.Model,
		}
	}
	return out
}

// handleSettingsTestRender checks that the configured headless browser is
// reachable and actually renders a page, so the operator finds out here
// instead of through empty shopping lists.
func (s *Server) handleSettingsTestRender(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	rc := s.renderConfig(ctx)
	if !rc.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "No headless browser configured. Set RENDER_BACKEND and RENDER_URL above.",
		})
		return
	}

	// Every configured service is checked, not just the first: under "auto"
	// the fallback is the one that gets past a bot wall, and finding out it
	// is misconfigured during a shopping list is too late.
	type check struct {
		Backend    string `json:"backend"`
		URL        string `json:"url"`
		OK         bool   `json:"ok"`
		Error      string `json:"error,omitempty"`
		DurationMS int64  `json:"duration_ms"`
	}

	var checks []check
	allOK := true
	for _, renderer := range rc.Renderers() {
		start := time.Now()
		err := scrape.CheckOneRenderer(ctx, renderer)
		c := check{
			Backend:    renderer.Label(),
			URL:        renderer.URL,
			OK:         err == nil,
			DurationMS: time.Since(start).Milliseconds(),
		}
		if err != nil {
			c.Error = err.Error()
			allOK = false
		}
		checks = append(checks, c)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      allOK,
		"backend": rc.Label(),
		"checks":  checks,
	})
}
