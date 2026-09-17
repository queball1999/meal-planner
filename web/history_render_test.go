package web

import (
	"html/template"
	"strings"
	"testing"

	"goeat/db"
)

// TestHistoryPageExecutes renders /plan/history against a filled-in
// historyPageData - both a regular row and a failed, still-regeneratable one
// (CanRegenerate: true), so the retry-button branch and the row-click detail
// modal markup are exercised, not just parsed.
func TestHistoryPageExecutes(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/history.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	type historyPageData struct {
		Plans  []historyPlanRow
		From   string
		To     string
		Page   Pagination
		HasLLM bool
	}

	data := pageData{
		AppName: "Go Eat",
		Data: historyPageData{
			HasLLM: true,
			Plans: []historyPlanRow{
				{Plan: &db.Plan{ID: 1, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", Status: "ready", TotalCents: 9000, BudgetCents: 10000}},
				{Plan: &db.Plan{ID: 2, WeekStart: "2026-01-12", WeekEnd: "2026-01-18", Status: "error", BudgetCents: 10000}, CanRegenerate: true},
			},
		},
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		t.Fatalf("execute: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		`data-history-detail="1"`,
		`data-history-detail="2"`,
		`Retry generating this plan`, // CanRegenerate row gets a retry button, not the disabled alert icon
		`data-modal-open="must-include-modal"`, // retry opens the same picker/confirm modal, not a bare form POST
		`data-week="2026-01-12"`,
		`id="history-detail-modal"`,
		`/plan/history/`, // JS fetch target prefix
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered history page missing %q", want)
		}
	}
	// A CanRegenerate row without an LLM configured must not offer a retry
	// trigger that would silently no-op (must-include-modal isn't in the DOM
	// when HasLLM is false).
	noLLM := data
	noLLM.Data = historyPageData{
		HasLLM: false,
		Plans:  []historyPlanRow{{Plan: &db.Plan{ID: 3, WeekStart: "2026-01-12", WeekEnd: "2026-01-18", Status: "error"}, CanRegenerate: true}},
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", noLLM); err != nil {
		t.Fatalf("execute (no LLM): %v", err)
	}
	if strings.Contains(buf.String(), `data-modal-open="must-include-modal"`) {
		t.Error("retry trigger rendered with no LLM configured (must-include-modal isn't in the DOM)")
	}
}
