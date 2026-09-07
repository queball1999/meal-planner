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
			Processes: []backgroundProcess{
				{Name: "HTTP server", Description: "Serves the web app.", Status: "ok", StatusLabel: "Serving", Detail: "listening on :8080"},
				{Name: "Home Assistant sync", Description: "Pulls items back.", Status: "off", StatusLabel: "Not configured", Detail: "no base URL/token set"},
				{Name: "Auto-plan generation", Description: "Generates next week.", Status: "warn", StatusLabel: "Waiting on AI provider", Detail: "AUTO_PLAN_HOUR is set but no LLM is configured"},
			},
		},
	})
	for _, want := range []string{
		"Go Eat", "go1.23", "Reachable", "openai", "gpt-5",
		"HTTP server", "Home Assistant sync", "Auto-plan generation",
		"Not configured", "Waiting on AI provider",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("about page output missing %q", want)
		}
	}
}
