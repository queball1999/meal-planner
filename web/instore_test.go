package web

import (
	"strings"
	"testing"

	"goeat/db"
)

// The aisle order is the order a shop is walked, not the order the database
// hands rows back. Produce first, frozen late so it spends least time in the
// trolley.
func TestAisleForOrdersTheWalk(t *testing.T) {
	produce, _ := aisleFor("Produce")
	bakery, _ := aisleFor("Bakery")
	meat, _ := aisleFor("Meat & Seafood")
	dairy, _ := aisleFor("Dairy & Eggs")
	pantry, _ := aisleFor("Pantry")
	frozen, _ := aisleFor("Frozen")

	if !(produce < bakery && bakery < meat && meat < dairy && dairy < pantry && pantry < frozen) {
		t.Errorf("walk order wrong: produce=%d bakery=%d meat=%d dairy=%d pantry=%d frozen=%d",
			produce, bakery, meat, dairy, pantry, frozen)
	}
}

// Category is free text on the item form, so the match has to be tolerant.
func TestAisleForMatchesLoosely(t *testing.T) {
	want, _ := aisleFor("Produce")
	for _, variant := range []string{"produce", "Fresh Produce", "PRODUCE & HERBS", " fruit "} {
		if got, _ := aisleFor(variant); got != want {
			t.Errorf("aisleFor(%q) = %d, want the produce aisle %d", variant, got, want)
		}
	}
}

// A frozen product names two categories at once. The freezer is where it is.
func TestAisleForPutsFrozenInTheFreezer(t *testing.T) {
	frozen, name := aisleFor("Frozen Vegetables")
	wantFrozen, _ := aisleFor("Frozen")
	if frozen != wantFrozen || name != "Frozen" {
		t.Errorf("aisleFor(Frozen Vegetables) = %d/%q, want the frozen aisle", frozen, name)
	}
}

// An uncategorised item is the one most likely to be somewhere unexpected;
// leading the list with it sends a shopper to the wrong end of the shop first.
func TestAisleForPutsUnknownLast(t *testing.T) {
	n, name := aisleFor("")
	if n != aisleUnknown {
		t.Errorf("an empty category sorts at %d, want last (%d)", n, aisleUnknown)
	}
	if name != "Everything else" {
		t.Errorf("unknown aisle is called %q", name)
	}
	if n2, _ := aisleFor("Novelty Hats"); n2 != aisleUnknown {
		t.Errorf("an unrecognised category sorts at %d, want last", n2)
	}
}

func TestInstoreGroupingOrderAndPantrySkip(t *testing.T) {
	frozenID, produceID, pantryID := int64(1), int64(2), int64(3)
	cats := map[int64]string{
		frozenID:  "Frozen",
		produceID: "Produce",
		pantryID:  "Pantry",
	}
	lines := []*db.ShoppingListItem{
		{ID: 10, ItemID: &frozenID},
		{ID: 11, ItemID: &pantryID},
		{ID: 12, ItemID: &produceID},
		{ID: 13}, // no item, so no category
		// Already in the pantry: not being bought, so it has no business on a
		// list you are walking round a shop with.
		{ID: 14, ItemID: &produceID, InPantry: true},
	}

	got := instoreLinesFor(lines, cats)
	want := []string{"Produce", "Pantry", "Frozen", "Everything else"}
	if len(got) != len(want) {
		t.Fatalf("aisles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("aisle %d = %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

// An already-have line must not create an aisle of its own.
func TestInstoreSkipsAPantryOnlyAisle(t *testing.T) {
	id := int64(1)
	got := instoreLinesFor([]*db.ShoppingListItem{
		{ID: 1, ItemID: &id, InPantry: true},
	}, map[int64]string{id: "Frozen"})

	if len(got) != 0 {
		t.Errorf("aisles = %v, want none - every line was already in the pantry", got)
	}
}

func TestInstorePageRenders(t *testing.T) {
	out := renderPage(t, "instore", pageData{
		AppName: "Go Eat",
		Page:    "list",
		User:    &db.User{Username: "sam"},
		Data: instorePageData{
			HasPlan: true, WeekStart: "2026-01-04",
			Remaining: 2, Count: 3, Total: "$40.00", InCart: "$12.00",
			Aisles: []instoreAisle{
				{Name: "Produce", Remaining: 2, Lines: []instoreLine{
					{ID: 1, Name: "Yellow onion", Qty: "2 each", Price: "$1.50"},
					{ID: 2, Name: "Spinach", Qty: "1 bag", Price: "$3.00"},
				}},
				{Name: "Frozen", Remaining: 0, Lines: []instoreLine{
					{ID: 3, Name: "Peas", Qty: "1 bag", Price: "$2.00", Checked: true},
				}},
			},
		},
	})

	for _, want := range []string{
		"Produce", "Frozen", "Yellow onion",
		`data-instore-row`, `data-id="1"`,
		`aria-pressed="false"`, `aria-pressed="true"`,
		// A finished aisle stays reachable, dimmed - it has to be, to undo a
		// mis-tap.
		"instore-aisle--done",
		"instore-row--checked",
		"/static/js/instore.js",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("in-store page missing %q", want)
		}
	}
}

func TestInstorePageWithNoPlan(t *testing.T) {
	out := renderPage(t, "instore", pageData{
		AppName: "Go Eat", Page: "list",
		User: &db.User{Username: "sam"},
		Data: instorePageData{},
	})
	if !strings.Contains(out, "Nothing to shop for yet") {
		t.Error("the no-plan state is not rendered")
	}
	if strings.Contains(out, "instore-bar") {
		t.Error("the running-total bar renders with no plan")
	}
}

// Every line covered by the pantry means a real, and pleasant, empty state -
// not a blank page.
func TestInstorePageWithNothingToBuy(t *testing.T) {
	out := renderPage(t, "instore", pageData{
		AppName: "Go Eat", Page: "list",
		User: &db.User{Username: "sam"},
		Data: instorePageData{HasPlan: true, Total: "$0.00", InCart: "$0.00"},
	})
	if !strings.Contains(out, "Nothing to buy") {
		t.Error("the nothing-to-buy state is not rendered")
	}
}
