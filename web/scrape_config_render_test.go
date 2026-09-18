package web

import (
	"html/template"
	"strings"
	"testing"

	"goeat/db"
)

// TestScrapeConfigPageExecutes renders the Scrape Config page for a store
// that is blocked on a bot wall - parsing alone would not catch a field
// referenced from the wrong scope (e.g. .Config.Blocked, .Data.HasBrowserless
// inside the per-store range) or a captchaCookieForm define name collision;
// that only fails at execution time.
func TestScrapeConfigPageExecutes(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/scrape_config.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data := pageData{
		AppName: "Go Eat",
		Data: scrapePageData{
			HasLLM:         true,
			HasBrowserless: true,
			Stores: []scrapeStoreRow{
				{
					Store: &db.GroceryStore{ID: 1, Name: "Blocked Mart"},
					Config: &db.ScrapeConfig{
						ID: 1, StoreID: 1, SearchURLTemplate: "https://blocked.example.com/search?q={term}",
						Mode: "auto", Status: "degraded",
						BlockReason: "Cloudflare interstitial", BlockedAt: "2026-01-01T00:00:00Z",
					},
				},
				{
					Store:  &db.GroceryStore{ID: 2, Name: "Fine Mart"},
					Config: &db.ScrapeConfig{ID: 2, StoreID: 2, SearchURLTemplate: "https://fine.example.com/search?q={term}", Mode: "auto", Status: "active"},
				},
				{Store: &db.GroceryStore{ID: 3, Name: "Unconfigured Mart"}},
			},
		},
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		t.Fatalf("execute: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"blocked - needs a human",
		"Cloudflare interstitial",
		`class="btn btn-danger btn-sm btn-solve-captcha" data-store="1"`,
		`id="captchaSolveModal"`,
		`id="captchaLiveStartBtn"`, // HasBrowserless: true renders the live-view path
		`id="captchaCookieForm"`,   // and the cookie-paste fallback is still offered via <details>
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered scrape config page missing %q", want)
		}
	}
	// The healthy store must not get a blocked badge or solve button - count
	// the actual button element, not the modal script's own ".btn-solve-captcha"
	// selector string, which also contains the class name.
	const solveButtonMarker = `class="btn btn-danger btn-sm btn-solve-captcha"`
	if n := strings.Count(out, solveButtonMarker); n != 1 {
		t.Errorf("expected exactly one solve-captcha button (blocked store only), got %d", n)
	}
}

// TestScrapeConfigPageExecutesWithoutBrowserless covers the HasBrowserless:
// false branch, where the modal should skip straight to the cookie-paste
// form instead of offering a live session.
func TestScrapeConfigPageExecutesWithoutBrowserless(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/scrape_config.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data := pageData{
		AppName: "Go Eat",
		Data: scrapePageData{
			HasBrowserless: false,
			Stores: []scrapeStoreRow{
				{
					Store: &db.GroceryStore{ID: 1, Name: "Blocked Mart"},
					Config: &db.ScrapeConfig{
						ID: 1, StoreID: 1, SearchURLTemplate: "https://blocked.example.com/search?q={term}",
						Mode: "auto", Status: "degraded",
						BlockReason: "HTTP 403 - blocked as a bot", BlockedAt: "2026-01-01T00:00:00Z",
					},
				},
			},
		},
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		t.Fatalf("execute: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, `id="captchaLiveStartBtn"`) {
		t.Error("live-session button should not render when HasBrowserless is false")
	}
	if !strings.Contains(out, `id="captchaCookieForm"`) {
		t.Error("cookie-paste fallback should still render when HasBrowserless is false")
	}
}
