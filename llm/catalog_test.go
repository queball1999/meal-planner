package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListModels_MergesOllamaAndOpenAIShapes covers a self-hosted
// OpenAI-compatible server (llama.cpp, Ollama's compat shim) that answers
// /v1/models with both an Ollama-style "models" array (name/model fields)
// and an OpenAI-style "data" array (id field) in the same body. Both must be
// read - a server that only ever sent the "models" shape used to come back
// empty, since only "data[].id" was parsed.
func TestListModels_MergesOllamaAndOpenAIShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"models": [{"name": "gemma-4/26b-a4b-it", "model": "gemma-4/26b-a4b-it"}],
			"object": "list",
			"data": [{"id": "gemma-4/26b-a4b-it"}]
		}`))
	}))
	t.Cleanup(srv.Close)

	models, err := ListModels(t.Context(), "openai_compatible", "", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0] != "gemma-4/26b-a4b-it" {
		t.Fatalf("models = %v, want exactly [gemma-4/26b-a4b-it] (deduped across both shapes)", models)
	}
}

// TestListModels_OllamaShapeOnly is the same case with no "data" array at
// all - the shape a plain Ollama-style server (not just an OpenAI-compat
// shim also emitting one) would send.
func TestListModels_OllamaShapeOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models": [{"name": "llama3.2:latest"}, {"model": "no-name-model"}]}`))
	}))
	t.Cleanup(srv.Close)

	models, err := ListModels(t.Context(), "openai_compatible", "", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := map[string]bool{"llama3.2:latest": true, "no-name-model": true}
	if len(models) != 2 {
		t.Fatalf("models = %v, want 2 entries", models)
	}
	for _, m := range models {
		if !want[m] {
			t.Errorf("unexpected model %q", m)
		}
	}
}
