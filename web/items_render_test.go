package web

import (
	"html/template"
	"strings"
	"testing"

	"goeat/db"
)

func renderPage(t *testing.T, name string, data pageData) string {
	t.Helper()
	tmpl, err := template.New("").
		Funcs(templateFuncs()).
		ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", "templates/"+name+".html")
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	return buf.String()
}

func TestItemsPageExecutes(t *testing.T) {
	out := renderPage(t, "items", pageData{
		AppName: "Go Eat",
		Page:    "items",
		Data: itemsPageData{
			Categories: []string{"Dairy & Eggs", "Produce"},
			Items: []itemRow{
				{Item: &db.Item{ID: 1, Name: "Sharp cheddar cheese", Category: "Dairy & Eggs", StockUnit: "g", Source: "builtin"}, ImageURL: "/static/img/items/placeholder.svg"},
				{Item: &db.Item{ID: 2, Name: "Yellow onion", Category: "Produce", StockUnit: "each", Source: "auto"}, ImageURL: "/item-images/x.jpg"},
			},
		},
	})
	for _, want := range []string{
		`href="/pantry/items/1"`,
		`Sharp cheddar cheese`,
		`subtab--active`, // pantry tab strip rendered, Items active
		`/static/img/items/placeholder.svg`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("items page missing %q", want)
		}
	}
}

func TestItemDetailPageExecutes(t *testing.T) {
	one := int64(7)
	out := renderPage(t, "item_detail", pageData{
		AppName: "Go Eat",
		Page:    "item_detail",
		Data: itemDetailPageData{
			Item:     &db.Item{ID: 7, Name: "Large eggs", NormalizedTerm: "eggs", Category: "Dairy & Eggs", StockUnit: "each", DefaultPurchaseQty: 12, Source: "builtin"},
			ImageURL: "/static/img/items/placeholder.svg",
			Stores:   []*db.GroceryStore{{ID: 3, Name: "Kroger"}},
			Packages: []itemPackageRow{
				{ItemStorePackage: &db.ItemStorePackage{ID: 1, ItemID: 7, StoreID: 3, PurchaseUnit: "dozen", AmountPerPackage: 12, PriceCents: 349}, StoreName: "Kroger", PriceLabel: "$3.49"},
			},
			Conversions: []*db.UnitConversion{
				{ID: 5, ItemID: &one, FromUnit: "each", ToUnit: "g", Factor: 50},
			},
			UnitOptions: commonUnits,
		},
	})
	for _, want := range []string{
		`action="/pantry/items/7/edit"`,
		`action="/pantry/items/7/packages"`,
		`action="/pantry/items/7/conversions"`,
		`action="/pantry/items/7/packages/1/delete"`,
		`action="/pantry/items/7/conversions/5/delete"`,
		`data-choices`,
		`Kroger`,
		`$3.49`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("item detail page missing %q", want)
		}
	}
}

// "Add item" moved out of an inline card into a modal opened from the top
// right, matching the recipes page, and every row gained a quick-add action.
func TestItemsPageUsesAddModalAndQuickAdd(t *testing.T) {
	out := renderPage(t, "items", pageData{
		AppName: "Go Eat",
		Page:    "items",
		Data: itemsPageData{
			Categories: []string{"Dairy & Eggs"},
			Items: []itemRow{
				{Item: &db.Item{ID: 7, Name: "Sharp cheddar cheese", StockUnit: "g", DefaultPurchaseQty: 200, Source: "builtin"}},
			},
		},
	})

	for _, want := range []string{
		`data-modal-open="add-item"`,
		`id="add-item"`,
		`page-header--actions`,
		// The quick-add control carries what the dialog needs to prefill.
		`data-quick-add`,
		`data-id="7"`,
		`data-unit="g"`,
		`data-qty="200"`,
		`id="quick-add"`,
		`id="quick-add-form"`,
		`data-quick-dest="pantry"`,
		`data-quick-dest="list"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("items page missing %q", want)
		}
	}

	// The old always-visible add card is gone.
	if strings.Contains(out, `<h2 class="form-section__title">Add item</h2>`) {
		t.Error("the inline Add item card is still rendered")
	}
}

func TestPantryPageUsesAddModal(t *testing.T) {
	out := renderPage(t, "pantry", pageData{
		AppName: "Go Eat",
		Page:    "pantry",
		Data:    pantryPageData{},
	})

	for _, want := range []string{
		`data-modal-open="add-pantry"`,
		`id="add-pantry"`,
		`page-header--actions`,
		// The barcode scanner keeps its place in the header rather than being
		// buried in the modal.
		`href="/scan"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pantry page missing %q", want)
		}
	}
}

// stores.html carried its own modal system - markup, opener, closer, Escape
// handler. It now uses the shared chrome, so modal.js owns all four.
func TestStoresPageUsesSharedModalChrome(t *testing.T) {
	out := renderPage(t, "stores", pageData{
		AppName: "Go Eat",
		Page:    "stores",
		Data:    storesPageData{},
	})

	if !strings.Contains(out, `id="add-store-modal"`) {
		t.Error("the add-store dialog is missing")
	}
	if !strings.Contains(out, `class="modal-overlay"`) {
		t.Error("the add-store dialog is not on the shared chrome")
	}
	for _, dead := range []string{"modal__backdrop", "modal__panel", "modal__head", "document.body.style.overflow"} {
		if strings.Contains(out, dead) {
			t.Errorf("stores page still carries its old modal system (%q)", dead)
		}
	}
}
