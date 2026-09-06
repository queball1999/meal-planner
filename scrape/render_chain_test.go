package scrape

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func autoConfig(browserless, flaresolverr string) RenderConfig {
	return RenderConfig{
		Backend:         RendererAuto,
		URL:             browserless,
		FlareSolverrURL: flaresolverr,
	}
}

func TestChainOrdersByChallengeType(t *testing.T) {
	cfg := autoConfig("http://browserless:3000", "http://flaresolverr:8191")

	// Browserless leads whenever it is configured: it is the only one that
	// runs the page's JavaScript, and FetchSmart can hand it FlareSolverr's
	// clearance when a wall turns it away.
	for _, reason := range []string{"Imperva/Incapsula challenge", "page renders its content in JavaScript"} {
		if got := cfg.Chain(reason); got[0].Backend != RendererBrowserless {
			t.Errorf("%s: chain led with %s, want browserless", reason, got[0].Backend)
		}
	}
	// With only FlareSolverr configured it obviously leads.
	only := RenderConfig{Backend: RendererFlareSolverr, URL: "http://flaresolverr:8191"}
	if got := only.Chain("Cloudflare interstitial"); len(got) != 1 || got[0].Backend != RendererFlareSolverr {
		t.Errorf("FlareSolverr-only chain = %+v", got)
	}
	if got := cfg.Chain("Cloudflare interstitial"); len(got) != 2 {
		t.Errorf("both services should stay in the chain, got %d", len(got))
	}
}

// A Browserless-only backend still uses a FlareSolverr URL when one is set -
// configuring both services is meant to be enough.
func TestFlareSolverrIsUsedAlongsideBrowserless(t *testing.T) {
	cfg := RenderConfig{
		Backend:         RendererBrowserless,
		URL:             "http://browserless:3000",
		FlareSolverrURL: "http://flaresolverr:8191",
	}
	rs := cfg.Renderers()
	if len(rs) != 2 {
		t.Fatalf("renderers = %d, want 2", len(rs))
	}
	if cfg.Label() != "Browserless + FlareSolverr" {
		t.Errorf("label = %q", cfg.Label())
	}
}

func TestRenderConfigDisabledWithoutURL(t *testing.T) {
	if (RenderConfig{Backend: RendererAuto}).Enabled() {
		t.Error("auto with no URLs should be disabled")
	}
	if (RenderConfig{Backend: RendererBrowserless, URL: "http://x:3000"}).Label() != "Browserless" {
		t.Error("a single backend should keep its plain label")
	}
}

// The whole point of running both: when the first service comes back still
// blocked, the second one gets a turn.
func TestFetchSmartFallsThroughToSecondRenderer(t *testing.T) {
	// Browserless POST /content returns rendered HTML directly. This one is
	// still behind the wall.
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>Pardon Our Interruption</body></html>`))
	}))
	defer blocked.Close()

	// FlareSolverr's wire shape: the solved page comes back under solution.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","solution":{"url":"https://store.example/x","status":200,"response":"<html><body><h1>Real prices</h1><ul><li>Eggs $3.99</li><li>Milk $2.49</li><li>Bread $2.99</li></ul><p>A page with enough real content that it does not read as an empty shell.</p></body></html>"}}`))
	}))
	defer good.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<html><body>Pardon Our Interruption</body></html>"))
	}))
	defer origin.Close()

	// Browserless leads. Here it is the one that is still blocked, so the
	// chain must move on to FlareSolverr rather than ending the fetch.
	// (No FlareSolverr URL is set as a clearance source in this config, so
	// the handoff is skipped and the plain chain continues.)
	cfg := RenderConfig{
		Backend:         RendererAuto,
		URL:             blocked.URL, // Browserless: still blocked
		FlareSolverrURL: good.URL,    // FlareSolverr: returns the real page
	}

	res, err := FetchSmart(context.Background(), origin.URL, cfg)
	if err != nil {
		t.Fatalf("FetchSmart: %v", err)
	}
	if !strings.Contains(res.HTML, "Real prices") {
		t.Fatalf("expected the second renderer's page, got %q", res.HTML)
	}
	if res.Backend != "FlareSolverr" {
		t.Errorf("Backend = %q, want FlareSolverr", res.Backend)
	}
	if res.Challenge != "" {
		t.Errorf("a page that came through clean should carry no challenge, got %q", res.Challenge)
	}
}
