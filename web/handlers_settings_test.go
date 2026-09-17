package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goeat/config"
	"goeat/db"
)

// TestHandleAIModels_UsesPostedURLOverSavedOne is the "Refresh list silently
// queries the old server" regression: editing the base URL field and
// clicking "Refresh list" before the 700ms autosave debounce (or its
// blur-flush) has actually persisted it used to still read the *previously
// saved* URL from the database, since handleAIModels ignored anything the
// browser posted. It must prefer a non-blank posted base_url over whatever
// is on file.
func TestHandleAIModels_UsesPostedURLOverSavedOne(t *testing.T) {
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"gemma-4/26b-a4b-it"}]}`))
	}))
	t.Cleanup(fresh.Close)

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	s := &Server{
		store: store,
		// The saved/stale URL: a stand-in for whatever the household's
		// previous llama.cpp server was, and unreachable, so a test that
		// still hit it would fail loudly rather than passing by accident.
		cfg: &config.Config{Provider: "openai_compatible", LLMAPIUrl: "http://127.0.0.1:1/stale"},
	}

	body := `{"provider":"openai_compatible","base_url":"` + fresh.URL + `"}`
	r := httptest.NewRequest(http.MethodPost, "/settings/ai/models", strings.NewReader(body))
	w := httptest.NewRecorder()

	s.handleAIModels(w, r)

	var resp struct {
		OK     bool     `json:"ok"`
		Models []string `json:"models"`
		Source string   `json:"source"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}

	if resp.Source != "live" {
		t.Fatalf("source = %q (error: %q), want \"live\" from the posted URL, not a fallback to the stale saved one", resp.Source, resp.Error)
	}
	if len(resp.Models) != 1 || resp.Models[0] != "gemma-4/26b-a4b-it" {
		t.Fatalf("models = %v, want [gemma-4/26b-a4b-it] from the freshly posted URL", resp.Models)
	}
}
