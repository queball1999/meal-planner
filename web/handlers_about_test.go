package web

import (
	"strings"
	"testing"
	"time"
)

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{90 * time.Second, "1m 30s"},
		{2*time.Hour + 5*time.Minute, "2h 5m"},
		{3*24*time.Hour + 4*time.Hour + 12*time.Minute, "3d 4h 12m"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.want {
			t.Errorf("formatUptime(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestHumaniseSince(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{2 * time.Hour, "2h ago"},
		{3 * 24 * time.Hour, "3d ago"},
	}
	for _, c := range cases {
		if got := humaniseSince(c.d); got != c.want {
			t.Errorf("humaniseSince(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestAboutPage_Renders is a template-execution smoke test for the About
// page, including a mix of "ok"/"warn"/"off" background-process rows.
func TestAboutPage_Renders(t *testing.T) {
	out := renderPage(t, "about", pageData{
		AppName: "Go Eat",
		Page:    "about",
		Data: aboutPageData{
			AppName: "Go Eat", Version: "dev", GoVersion: "go1.23", NumCPU: 4,
			Uptime: "1h 2m", NumGoroutine: 12, MemAllocMB: 5.4, MemSysMB: 20.1, NumGC: 3,
			DBReachable:   true,
			LLMConfigured: true, LLMProvider: "openai", LLMModel: "gpt-5",
			Probes: probePlaceholders(),
			Processes: []backgroundProcess{
				{Name: "HTTP server", Description: "Serves the web app.", Status: "ok", StatusLabel: "Serving", Detail: "listening on :8080"},
				{Name: "Home Assistant sync", Description: "Pulls items back.", Status: "off", StatusLabel: "Not configured", Detail: "no base URL/token set"},
				{Name: "Auto-plan generation", Description: "Generates next week.", Status: "warn", StatusLabel: "Waiting on AI provider", Detail: "AUTO_PLAN_HOUR is set but no LLM is configured"},
			},
		},
	})
	for _, want := range []string{
		"Go Eat", "go1.23",
		"HTTP server", "Home Assistant sync", "Auto-plan generation",
		"Not configured", "Waiting on AI provider",
		// The connectivity rows are placeholders now: each is a real request
		// to the service, filled in by /about/probe rather than during the
		// page render, so one that is down cannot hold the page up.
		`data-probe="database"`, `data-probe="llm"`,
		`data-probe="home_assistant"`, `data-probe="renderer"`,
		"Checking…", "/about/probe",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("about page output missing %q", want)
		}
	}
	// The old widget painted a badge background behind the value, inside a
	// chip that already has a surface. Scoped to stats-chip__value: the
	// background-processes table below uses a real standalone .badge pill,
	// which is the right thing there.
	for _, dead := range []string{
		`stats-chip__value badge-success`,
		`stats-chip__value badge-danger`,
		`stats-chip__value badge-muted`,
	} {
		if strings.Contains(out, dead) {
			t.Errorf("connectivity widget still paints a background behind its value (%q)", dead)
		}
	}
}

func TestProbePlaceholdersMatchStreamedKeys(t *testing.T) {
	// A placeholder whose key the stream never sends stays on "Checking…"
	// forever, which reads as a hung page rather than a wiring mistake.
	placeholders := probePlaceholders()
	if len(placeholders) != len(probeRows) {
		t.Fatalf("%d placeholders for %d probe rows", len(placeholders), len(probeRows))
	}
	for i, p := range placeholders {
		if p.Key != probeRows[i].Key {
			t.Errorf("placeholder %d has key %q, probe row has %q", i, p.Key, probeRows[i].Key)
		}
		if p.Status != "checking" {
			t.Errorf("placeholder %q starts as %q, want checking", p.Key, p.Status)
		}
		if probeRows[i].Run == nil {
			t.Errorf("probe row %q has no check to run", p.Key)
		}
	}
}
