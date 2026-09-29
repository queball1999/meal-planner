package web

import (
	"html/template"
	"strings"
	"testing"

	"goeat/db"
)

// TestStoresPageExecutes renders the Stores page with two stores, a
// shopping split and an item tied to one store, so a field referenced from
// the wrong scope inside the per-store ranges fails here, not in a browser.
func TestStoresPageExecutes(t *testing.T) {
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/stores.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data := pageData{
		AppName: "Go Eat",
		Data: storesPageData{
			ShareTotal: 100,
			Stores: []storeRow{
				{
					Store:          &db.GroceryStore{ID: 1, Name: "Aldi", Kind: "grocery", SharePct: 70},
					Hue:            1,
					PreferredItems: []preferredItemLink{{ItemID: 9, Name: "coffee"}},
					ItemOptions: []storeItemOption{
						{ID: 9, Name: "coffee", Selected: true},
						{ID: 10, Name: "tofu", Elsewhere: "Co-op"},
					},
				},
				{Store: &db.GroceryStore{ID: 2, Name: "Co-op", Kind: "grocery", SharePct: 30}, Hue: 2},
			},
		},
	}

	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`name="share_1" value="70"`,
		`name="share_2" value="30"`,
		`action="/stores/1/items"`,
		`<option value="9" selected>coffee</option>`,
		`tofu (now: Co-op)`,
		`id="store-items-2"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered stores page missing %q", want)
		}
	}
}
