package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"goeat/config"
)

func TestSharedURL(t *testing.T) {
	cases := []struct {
		q    url.Values
		want string
	}{
		{url.Values{"url": {"https://www.youtube.com/shorts/abc"}}, "https://www.youtube.com/shorts/abc"},
		{url.Values{"text": {"Check out this video! https://vm.tiktok.com/ZMabc/ #dinner"}}, "https://vm.tiktok.com/ZMabc/"},
		{url.Values{"title": {"Chili"}, "text": {"(https://example.com/chili)."}}, "https://example.com/chili"},
		{url.Values{"text": {"no link here"}}, ""},
	}
	for _, c := range cases {
		if got := sharedURL(c.q); got != c.want {
			t.Errorf("sharedURL(%v) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestRecipeShareRedirectsToPrefilledImport(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	s.handleRecipeShare(rec, httptest.NewRequest("GET", "/recipes/share?text="+url.QueryEscape("look https://vm.tiktok.com/ZMabc/"), nil))
	if loc := rec.Header().Get("Location"); loc != "/recipes/import?url="+url.QueryEscape("https://vm.tiktok.com/ZMabc/") {
		t.Errorf("Location = %q", loc)
	}
}

func TestManifestIsServerOnly(t *testing.T) {
	desktop := &Server{cfg: &config.Config{Desktop: true, AppName: "Go Eat"}}
	for _, h := range []http.HandlerFunc{desktop.handleManifest, desktop.handleServiceWorker} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("desktop: status %d, want 404", rec.Code)
		}
	}

	server := &Server{cfg: &config.Config{AppName: "Go Eat"}}
	rec := httptest.NewRecorder()
	server.handleManifest(rec, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	var m struct {
		Name        string `json:"name"`
		ShareTarget struct {
			Action string `json:"action"`
			Method string `json:"method"`
		} `json:"share_target"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if m.Name != "Go Eat" || m.ShareTarget.Action != "/recipes/share" || m.ShareTarget.Method != "GET" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestShareAvailability(t *testing.T) {
	plain := httptest.NewRequest("GET", "http://goeat.lan/recipes/import", nil)
	proxied := httptest.NewRequest("GET", "http://goeat.lan/recipes/import", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	cases := []struct {
		name string
		cfg  config.Config
		r    *http.Request
		want string
	}{
		{"desktop", config.Config{Desktop: true, PublicBaseURL: "https://x.example"}, plain, "desktop"},
		{"plain http", config.Config{}, plain, "needs_https"},
		{"https base url", config.Config{PublicBaseURL: "https://goeat.example.com"}, plain, "ready"},
		{"behind a TLS proxy", config.Config{}, proxied, "ready"},
	}
	for _, c := range cases {
		s := &Server{cfg: &c.cfg}
		if got := s.shareAvailability(c.r).Mode; got != c.want {
			t.Errorf("%s: mode %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRecipeImportPageShareCard(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/recipe_import.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		mode, url string
		want      []string
	}{
		{"desktop", "", []string{"Not available in this deployment", "Docker image from GitHub", dockerGuideURL}},
		{"needs_https", "", []string{"Not available in this deployment", "PUBLIC_BASE_URL"}},
		{"ready", "https://vm.tiktok.com/ZMabc/", []string{"Install app", `value="https://vm.tiktok.com/ZMabc/"`, "Shared link ready"}},
	}
	for _, c := range cases {
		var buf strings.Builder
		data := pageData{AppName: "Go Eat", WebApp: c.mode != "desktop", Data: recipeImportPageData{
			Video: videoImportState{HasLLM: true, Capability: "full"},
			Share: shareTargetState{Mode: c.mode, DockerURL: dockerGuideURL},
			URL:   c.url,
		}}
		if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
			t.Fatalf("%s: execute: %v", c.mode, err)
		}
		out := buf.String()
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q", c.mode, w)
			}
		}
		if hasManifest := strings.Contains(out, `rel="manifest"`); hasManifest != (c.mode != "desktop") {
			t.Errorf("%s: manifest linked = %v", c.mode, hasManifest)
		}
	}
}
