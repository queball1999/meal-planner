package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"goeat/db"
	"goeat/llm"
	"goeat/middleware"
	"goeat/pricing"
	"goeat/scrape"
	"goeat/settings"
)

// settingsFieldView is one editable row on the Settings page.
type settingsFieldView struct {
	Key     string
	Role    string // AI fields only: "key" | "model" | "url"
	Label   string
	Help    string
	Tooltip string
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
	ActiveLabel  string // display name of the ActiveID provider, for the "set as default" confirm prompt
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
			Tooltip: d.Tooltip,
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
		if d.Kind == settings.KindSecret && d.Encrypted && s.box != nil {
			if ct, ok, _ := s.store.GetSecret(r.Context(), d.Key); ok && ct != "" {
				field.IsSet = true
				field.Source = "admin"
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

	activeLabel := active
	if info, ok := llm.ProviderByID(active); ok {
		activeLabel = info.Label
	}

	// Scraping lives on its own tab now (/admin/scrape's "Headless browser &
	// anti-bot" card, built from settingsFieldsForCategory below) rather than
	// as a plain category card here - it belongs next to the per-store
	// scraping config it configures, not among the General settings.
	generalCategories := make([]settingsCategoryView, 0, len(categories))
	for _, c := range categories {
		if c.Name == "Scraping" {
			continue
		}
		generalCategories = append(generalCategories, c)
	}

	s.render(w, r, "settings", settingsPageData{
		HasLLM:       s.llmGen() != nil,
		ActiveID:     active,
		ActiveLabel:  activeLabel,
		Providers:    providers,
		SharedFields: sharedAI,
		Categories:   generalCategories,
	})
}

// settingsFieldsForCategory resolves every settings.Defs entry in one
// category to its current settingsFieldView - the same per-field resolution
// handleSettingsPage's own category loop does, pulled out so the Scraping
// page's "Headless browser & anti-bot" card can render just that one
// category outside the otherwise-general Settings page.
func (s *Server) settingsFieldsForCategory(ctx context.Context, byKey map[string]*db.Setting, category string) []settingsFieldView {
	var fields []settingsFieldView
	for _, d := range settings.Defs {
		if d.Category != category {
			continue
		}
		field := settingsFieldView{
			Key: d.Key, Role: d.Role, Label: d.Label, Help: d.Help,
			Kind: kindNames[d.Kind], Options: d.Options,
		}
		if row := byKey[d.Key]; row != nil {
			field.Source = row.Source
			if d.Kind == settings.KindSecret {
				field.IsSet = row.Value != ""
			} else {
				field.Value = row.Value
			}
		}
		if d.Kind == settings.KindSecret && d.Encrypted && s.box != nil {
			if ct, ok, _ := s.store.GetSecret(ctx, d.Key); ok && ct != "" {
				field.IsSet = true
				field.Source = "admin"
			}
		}
		fields = append(fields, field)
	}
	return fields
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
//	POST /settings/ai/models  {"provider":"openai","base_url":"…","api_key":"…"}
//	→ {"ok":true,"models":[...],"source":"live"|"catalog","error":"…"}
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		// BaseURL/APIKey are whatever is currently typed into the form, sent
		// straight from the browser - not read back from the server. Refresh
		// used to always read the saved values instead, which raced the
		// autosave debounce: editing the URL and immediately clicking
		// "Refresh list" (before the 700ms debounce, or its blur-flush,
		// finished) silently listed models from the *previous* URL. Only
		// override the saved value when the field isn't blank, since a
		// password field reads blank once its secret is already saved.
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
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

	cfg := *s.cfg
	if err := settings.Apply(r.Context(), s.store, &cfg, func(string, ...any) {}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "failed to read settings"})
		return
	}
	apiKey, baseURL, _ := llm.ProviderCreds(&cfg, info.ID)
	if v := strings.TrimSpace(body.BaseURL); v != "" {
		baseURL = v
	}
	if v := strings.TrimSpace(body.APIKey); v != "" {
		apiKey = v
	}

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

	// Encrypted secrets go to the `secrets` table (cryptbox-sealed), not the
	// plaintext settings table.
	if def.Kind == settings.KindSecret && def.Encrypted {
		if s.box == nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "encryption unavailable"})
			return
		}
		sealed, err := s.box.Seal(body.Value)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "encrypt failed"})
			return
		}
		if err := s.store.SetSecret(r.Context(), def.Key, sealed); err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "save failed"})
			return
		}
		// Never the value itself for a secret - just that it changed.
		s.logSettingsEvent(r, "setting.changed", def.Key, "secret updated")
		if def.Category == "AI Provider" {
			s.reloadLLM(r.Context())
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}

	stored, err := settings.SealValue(def, body.Value)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "encrypt failed"})
		return
	}
	if err := s.store.SetSetting(r.Context(), def.Key, stored); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "save failed"})
		return
	}

	// A secret is never written to the audit log by value (QSS §5.3).
	detail := "set to " + body.Value
	if def.Kind == settings.KindSecret {
		detail = "secret updated"
	}
	s.logSettingsEvent(r, "setting.changed", def.Key, detail)
	// Provider/model/key/sampling changes take effect immediately - see
	// reloadLLM. Every other setting on this page still needs a restart.
	if def.Category == "AI Provider" {
		s.reloadLLM(r.Context())
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// logSettingsEvent records a Settings-page change to the audit log. Settings
// are process-wide, not per-household, so there is no household to scope this
// to - just who made the change and what it was.
func (s *Server) logSettingsEvent(r *http.Request, action, targetID, detail string) {
	var uid *int64
	if u := middleware.UserFromCtx(r); u != nil {
		id := u.ID
		uid = &id
	}
	s.logEvent(r, uid, action, "setting", targetID, detail)
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

	// Build a generator fresh from whatever is saved right now, rather than
	// using s.llmGen() (built once at server startup - like every other setting
	// on this page, a provider/model/key edit here only reaches s.llmGen() after
	// a restart). Without this, Test Connection silently exercised whatever
	// was running before your last edit instead of what you just picked,
	// which is exactly the mismatch that made a stale, deprecated model look
	// like the one just selected in the dropdown.
	cfg := *s.cfg
	if err := settings.Apply(r.Context(), s.store, &cfg, func(string, ...any) {}); err != nil {
		json.NewEncoder(w).Encode(result{OK: false, Prompt: testPrompt, Error: "failed to read settings"})
		return
	}
	gen, err := llm.NewGenerator(&cfg)
	if err != nil {
		json.NewEncoder(w).Encode(result{OK: false, Prompt: testPrompt, Error: err.Error()})
		return
	}

	start := time.Now()
	resp, err := gen.Generate(r.Context(), llm.GenerateRequest{
		System: "You are a helpful assistant.",
		Prompt: testPrompt,
	})
	ms := time.Since(start).Milliseconds()

	if err != nil {
		json.NewEncoder(w).Encode(result{OK: false, Prompt: testPrompt, Error: err.Error(), DurationMS: ms, Provider: gen.ProviderName(), Model: gen.ModelName()})
		return
	}

	json.NewEncoder(w).Encode(result{
		OK:         true,
		Prompt:     testPrompt,
		Response:   resp.Content,
		DurationMS: ms,
		Provider:   gen.ProviderName(),
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
	// TEMPORARY diagnostic - remove once the missing-audit-log-entries bug is
	// found. Confirms whether s.llmGen() (what plan generation actually uses)
	// is the same instance as the global the entries came from.
	log.Printf("[llm-debug] handleLLMDebugLog: entries=%d global_nil=%v llmGen_nil=%v", len(entries), llm.GlobalDebugLog == nil, s.llmGen() == nil)
	if len(entries) > 10 {
		entries = entries[:10]
	}
	json.NewEncoder(w).Encode(entries)
}

// auditLogEntry is one row of the Audit Log page - an LLM call or a web-scrape
// fetch, merged into one time-ordered list. Kind switches the template layout.
type auditLogEntry struct {
	Kind   string // "llm" | "scrape"
	At     string // "2006-01-02 15:04:05"
	atSort time.Time

	// LLM
	System     string
	Prompt     string
	Response   string
	Provider   string
	Model      string
	InputToks  int
	OutputToks int

	// Scrape
	URL        string
	Host       string
	Backend    string
	Challenge  string
	StatusCode int
	Bytes      int
	ViaProxy   bool

	// Shared
	Error      string
	DurationMS int64
}

// handleLLMLogPage renders the full paginated Audit Log - LLM calls and web
// scraping fetches together - with search + kind/status filters.
//
//	GET /admin/llm-log?kind=llm|scrape&status=ok|error&q=…
func (s *Server) handleLLMLogPage(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind") // "" (both) | "llm" | "scrape"
	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status") // "ok" | "error" | ""
	qLow := strings.ToLower(q)

	matchStatus := func(hasError bool) bool {
		switch status {
		case "ok":
			return !hasError
		case "error":
			return hasError
		default:
			return true
		}
	}

	var entries []auditLogEntry

	if kind == "" || kind == "llm" {
		for _, e := range llmDebugEntries() {
			if !matchStatus(e.Error != "") {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(e.Prompt+"\x00"+e.Response+"\x00"+e.System), qLow) {
				continue
			}
			entries = append(entries, auditLogEntry{
				Kind: "llm", At: e.At, atSort: parseLogTime(e.At),
				System: e.System, Prompt: e.Prompt, Response: e.Response,
				Provider: e.Provider, Model: e.Model,
				InputToks: e.InputToks, OutputToks: e.OutputToks,
				Error: e.Error, DurationMS: e.DurationMS,
			})
		}
	}

	if kind == "" || kind == "scrape" {
		for _, e := range scrape.GlobalDebugLog.Entries() {
			if !matchStatus(e.Error != "") {
				continue
			}
			if q != "" && !strings.Contains(
				strings.ToLower(e.URL+"\x00"+e.Host+"\x00"+e.Backend+"\x00"+e.Challenge+"\x00"+e.Error), qLow) {
				continue
			}
			entries = append(entries, auditLogEntry{
				Kind: "scrape", At: e.At.Format("2006-01-02 15:04:05"), atSort: e.At,
				URL: e.URL, Host: e.Host, Backend: e.Backend, Challenge: e.Challenge,
				StatusCode: e.StatusCode, Bytes: e.Bytes, ViaProxy: e.ViaProxy,
				Error: e.Error, DurationMS: e.DurationMS,
			})
		}
	}

	// Newest first across both sources.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].atSort.After(entries[j].atSort)
	})

	pageEntries, page := paginate(r, entries)

	type llmLogPageData struct {
		Entries []auditLogEntry
		Query   string
		Status  string
		Kind    string
		Page    Pagination
	}

	s.render(w, r, "llm_log", llmLogPageData{
		Entries: pageEntries,
		Query:   q,
		Status:  status,
		Kind:    kind,
		Page:    page,
	})
}

// parseLogTime reads back the timestamp llmDebugEntries formats, for merge-sort
// against the scrape log's native time.Time.
func parseLogTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", s)
	return t
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

// handleSettingsTestKroger checks the saved Kroger credentials and location
// ID against the live API. Like Test AI it reads the settings as saved now,
// not the ones this process started with, so a fresh edit can be tested
// before the restart that actually applies it. An optional location_id in
// the body overrides the saved one, so a just-typed value is what gets tested.
//
//	POST /settings/test-kroger  {location_id?}
func (s *Server) handleSettingsTestKroger(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LocationID string `json:"location_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	cfg := *s.cfg
	if err := settings.Apply(r.Context(), s.store, &cfg, func(string, ...any) {}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "failed to read settings"})
		return
	}
	loc := strings.TrimSpace(body.LocationID)
	if loc == "" {
		loc = cfg.KrogerLocationID
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, pricing.CheckKroger(ctx, cfg.KrogerCredentials, loc))
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
