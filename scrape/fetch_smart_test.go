package scrape

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChallengeReason(t *testing.T) {
	realPage := `<html><body><div class="results">` +
		strings.Repeat(`<article class="card"><h3>Eggs</h3><span>$3.49</span></article>`, 60) +
		`</div></body></html>`

	cases := []struct {
		name   string
		html   string
		status int
		want   bool // want a challenge reported
	}{
		{"real page", realPage, 200, false},
		{"cloudflare interstitial", `<html><title>Just a moment...</title></html>`, 200, true},
		{"js+cookies wall", `<html><body>Enable JavaScript and cookies to continue</body></html>`, 200, true},
		{"403", realPage, 403, true},
		{"429", realPage, 429, true},
		{"js shell", `<html><body><div id="root"></div></body></html>`, 200, true},
	}
	for _, c := range cases {
		got := ChallengeReason(c.html, c.status)
		if (got != "") != c.want {
			t.Errorf("%s: ChallengeReason = %q, want challenge=%v", c.name, got, c.want)
		}
	}
}

// A Browserless-compatible stub: POST /content, rendered HTML back.
func browserlessStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.URL == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestRenderViaBrowserless(t *testing.T) {
	srv := browserlessStub(t, `<html><body><h1>Example Domain</h1></body></html>`)
	defer srv.Close()

	cfg := RenderConfig{Backend: RendererBrowserless, URL: srv.URL, Token: "secret"}
	res, err := Render(context.Background(), cfg, "https://example.com")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(res.HTML, "Example Domain") {
		t.Errorf("HTML = %q", res.HTML)
	}
	if err := CheckRenderer(context.Background(), cfg); err != nil {
		t.Errorf("CheckRenderer: %v", err)
	}
}

func TestRenderConfigEnabled(t *testing.T) {
	if (RenderConfig{}).Enabled() {
		t.Error("empty config should be disabled")
	}
	if (RenderConfig{Backend: RendererBrowserless}).Enabled() {
		t.Error("a backend with no URL should be disabled")
	}
	cfg := RenderConfig{Backend: RendererFlareSolverr, URL: "http://x:8191"}
	if !cfg.Enabled() || cfg.Label() != "FlareSolverr" {
		t.Errorf("cfg = %+v, label = %q", cfg, cfg.Label())
	}
}

// FetchSmart must escalate to the renderer when the direct fetch is a wall,
// and must report the reason when no renderer is configured.
func TestFetchSmartEscalatesToRenderer(t *testing.T) {
	wall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><title>Just a moment...</title></html>`))
	}))
	defer wall.Close()

	// safefetch blocks loopback addresses, so the direct hop fails outright -
	// which is itself the "escalate" path.
	rendered := `<html><body>` + strings.Repeat(`<article><span>$1.99</span></article>`, 60) + `</body></html>`
	br := browserlessStub(t, rendered)
	defer br.Close()

	res, err := FetchSmart(context.Background(), wall.URL,
		RenderConfig{Backend: RendererBrowserless, URL: br.URL})
	if err != nil {
		t.Fatalf("FetchSmart: %v", err)
	}
	if !res.ViaProxy || res.Backend != "Browserless" {
		t.Errorf("expected the renderer to serve it, got %+v", res)
	}
	if res.Challenge != "" {
		t.Errorf("rendered page still reported a challenge: %q", res.Challenge)
	}

	if _, err := FetchSmart(context.Background(), wall.URL, RenderConfig{}); err == nil {
		t.Error("expected an error when no renderer is configured and the fetch fails")
	}
}
