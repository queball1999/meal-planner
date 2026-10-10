package web

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaginateSlicesAndCounts(t *testing.T) {
	items := make([]int, 57)
	for i := range items {
		items[i] = i + 1
	}

	tests := []struct {
		name              string
		url               string
		wantFirst, wantN  int
		wantPage, wantTot int
		wantFrom, wantTo  int
	}{
		{"defaults", "/recipes", 1, 25, 1, 3, 1, 25},
		{"second page", "/recipes?page=2", 26, 25, 2, 3, 26, 50},
		{"last page is short", "/recipes?page=3", 51, 7, 3, 3, 51, 57},
		{"page past the end clamps", "/recipes?page=99", 51, 7, 3, 3, 51, 57},
		{"per_page honoured", "/recipes?per_page=10&page=2", 11, 10, 2, 6, 11, 20},
		{"odd per_page clamps up", "/recipes?per_page=7", 1, 10, 1, 6, 1, 10},
		{"huge per_page clamps down", "/recipes?per_page=9999", 1, 57, 1, 1, 1, 57},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.url, nil)
			got, p := paginate(r, items)
			if len(got) != tc.wantN {
				t.Errorf("rows = %d, want %d", len(got), tc.wantN)
			}
			if len(got) > 0 && got[0] != tc.wantFirst {
				t.Errorf("first row = %d, want %d", got[0], tc.wantFirst)
			}
			if p.Page != tc.wantPage || p.TotalPages != tc.wantTot {
				t.Errorf("page %d/%d, want %d/%d", p.Page, p.TotalPages, tc.wantPage, tc.wantTot)
			}
			if p.From != tc.wantFrom || p.To != tc.wantTo {
				t.Errorf("range %d-%d, want %d-%d", p.From, p.To, tc.wantFrom, tc.wantTo)
			}
			if p.Total != len(items) {
				t.Errorf("total = %d, want %d", p.Total, len(items))
			}
		})
	}
}

func TestPaginateEmptyList(t *testing.T) {
	r := httptest.NewRequest("GET", "/recipes", nil)
	got, p := paginate(r, []string(nil))
	if len(got) != 0 {
		t.Fatalf("rows = %d, want 0", len(got))
	}
	if p.HasPages() {
		t.Error("an empty list should not render a pager")
	}
	if p.From != 0 || p.To != 0 {
		t.Errorf("range %d-%d, want 0-0", p.From, p.To)
	}
}

// Page links must keep the filters that produced the list, or paging a
// filtered table silently widens it back to everything.
func TestPaginateCarriesFilters(t *testing.T) {
	r := httptest.NewRequest("GET", "/recipes?q=soup&source=ai&page=2&per_page=10", nil)
	_, p := paginate(r, make([]int, 40))

	if !strings.Contains(p.Query, "q=soup") || !strings.Contains(p.Query, "source=ai") {
		t.Errorf("Query %q dropped a filter", p.Query)
	}
	if strings.Contains(p.Query, "page=2") {
		t.Errorf("Query %q must not repeat the page parameter", p.Query)
	}
	if !strings.HasSuffix(p.Query, "&") {
		t.Errorf("Query %q must end in & so page=N can be appended", p.Query)
	}
	if _, ok := p.Carried["per_page"]; ok {
		t.Error("per_page must not be carried into the rows-per-page form")
	}
	if p.Carried["q"] != "soup" {
		t.Errorf("Carried[q] = %q, want soup", p.Carried["q"])
	}
}

// Two tables on one page (the Prices page) must page independently.
func TestPaginateNamedIsScoped(t *testing.T) {
	r := httptest.NewRequest("GET", "/admin/prices?page_1=2&per_page=10", nil)

	got1, p1 := paginateNamed(r, seq(30), "page_1")
	got2, p2 := paginateNamed(r, seq(30), "page_2")

	if p1.Page != 2 || got1[0] != 11 {
		t.Errorf("store 1: page %d starting at %d, want page 2 starting at 11", p1.Page, got1[0])
	}
	if p2.Page != 1 || got2[0] != 1 {
		t.Errorf("store 2: page %d starting at %d, want page 1 starting at 1", p2.Page, got2[0])
	}
	if !strings.Contains(p2.Query, "page_1=2") {
		t.Errorf("store 2 links dropped store 1's page: %q", p2.Query)
	}
}

func TestPaginationPartialRenders(t *testing.T) {
	tmpl, err := template.New("").Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/partials/*.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r := httptest.NewRequest("GET", "/recipes?q=soup&page=2", nil)
	_, p := paginate(r, seq(60))

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "pagination", p); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"q=soup&amp;page=3", "Showing", `name="per_page"`, `value="soup"`} {
		if !strings.Contains(out, want) {
			t.Errorf("pagination bar missing %q\n%s", want, out)
		}
	}
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

// The dashboard's plan history starts at five rows, pages on its own
// parameter, and every link lands back on the widget.
func TestPaginateSizedDashboardPlans(t *testing.T) {
	items := make([]int, 12)
	for i := range items {
		items[i] = i + 1
	}

	r := httptest.NewRequest("GET", "/?cal=month", nil)
	got, p := paginateSized(r, items, "plans_page", dashPlansPerPage, dashPlansPerPageChoices)
	if len(got) != 5 || p.PerPage != 5 || p.TotalPages != 3 {
		t.Fatalf("default: %d rows, per page %d, %d pages; want 5, 5, 3", len(got), p.PerPage, p.TotalPages)
	}
	p.Anchor = "plan-history"
	if link := string(p.Link(2)); link != "?cal=month&plans_page=2#plan-history" {
		t.Errorf("link = %q", link)
	}

	r = httptest.NewRequest("GET", "/?plans_page=2&per_page=5&page=9", nil)
	got, p = paginateSized(r, items, "plans_page", dashPlansPerPage, dashPlansPerPageChoices)
	if len(got) != 5 || got[0] != 6 || p.Page != 2 {
		t.Errorf("page 2: first=%v page=%d, want 6 and 2", got, p.Page)
	}

	r = httptest.NewRequest("GET", "/?per_page=3", nil)
	if _, p = paginateSized(r, items, "plans_page", dashPlansPerPage, dashPlansPerPageChoices); p.PerPage != 5 {
		t.Errorf("per_page=3 clamped to %d, want 5", p.PerPage)
	}
}
