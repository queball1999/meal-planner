package web

import (
	"strings"
	"testing"

	"goeat/db"
)

func TestMealColorClass_StableAndInRange(t *testing.T) {
	for _, title := range []string{"Chili", "Tacos", "Oatmeal with berries", ""} {
		c1 := mealColorClass(title)
		c2 := mealColorClass(title)
		if c1 != c2 {
			t.Fatalf("mealColorClass(%q) not stable: %q vs %q", title, c1, c2)
		}
		if !strings.HasPrefix(c1, "badge-hue-") {
			t.Fatalf("mealColorClass(%q) = %q, want badge-hue-N", title, c1)
		}
	}
}

func TestMealTagsFor_DedupesAndPreservesOrder(t *testing.T) {
	titles := map[int64]string{
		1: "Chili",
		2: "Tacos",
		3: "Chili", // same meal, different ingredient row
	}
	tags := mealTagsFor(`[1,2,3]`, titles)
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2 (deduped): %+v", len(tags), tags)
	}
	if tags[0].Title != "Chili" || tags[1].Title != "Tacos" {
		t.Fatalf("tags in wrong order: %+v", tags)
	}
	if tags[0].ColorClass == "" {
		t.Fatal("expected a non-empty color class")
	}
}

func TestMealTagsFor_EmptyInputs(t *testing.T) {
	if tags := mealTagsFor("", map[int64]string{1: "Chili"}); tags != nil {
		t.Fatalf("expected nil for empty refs JSON, got %+v", tags)
	}
	if tags := mealTagsFor(`[1]`, nil); tags != nil {
		t.Fatalf("expected nil for empty title map, got %+v", tags)
	}
	if tags := mealTagsFor(`not json`, map[int64]string{1: "Chili"}); tags != nil {
		t.Fatalf("expected nil for unparseable refs, got %+v", tags)
	}
}

// TestShoppingListBody_RendersMealPills is a template-execution smoke test:
// a line with Meals set should render one colored pill per distinct meal.
func TestShoppingListBody_RendersMealPills(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true,
			Tab:     "list",
			List: &shoppingListPageData{
				HasPlan:     true,
				TotalLabel:  "$10.00",
				BudgetLabel: "$50",
				Groups: []shoppingStoreGroup{
					{
						StoreName:     "Kroger",
						SubtotalLabel: "$10.00",
						Items: []shoppingLineItem{
							{
								ID:          1,
								DisplayName: "canned black beans",
								BuyLabel:    "1 can",
								TotalLabel:  "$1.29",
								BadgeClass:  "badge-estimate",
								BadgeText:   "Estimated",
								Meals: []mealTag{
									{Title: "Chili", ColorClass: "badge-hue-3"},
									{Title: "Taco Bowls", ColorClass: "badge-hue-6"},
								},
							},
						},
					},
				},
			},
		},
	})
	for _, want := range []string{
		`shopping-item__meals`,
		`badge-hue-3`,
		`Chili`,
		`badge-hue-6`,
		`Taco Bowls`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

// Regression: the estimated total was read from plan.total_cents, which only
// UpdatePlanTotal writes. Every other path that changes a price - the pencil
// editor, a headcount rescale, the unpriced-list fallback - left it stale, and
// a list full of priced lines reported "$0.00 estimated total".
func TestSummarizeLinesSumsCurrentLines(t *testing.T) {
	kroger, aldi := int64(7), int64(9)
	total, byStore := summarizeLines([]*db.ShoppingListItem{
		{ID: 1, LineTotalCents: 899, StoreID: &kroger},
		{ID: 2, LineTotalCents: 349, StoreID: &kroger},
		{ID: 3, LineTotalCents: 500, StoreID: &aldi},
		{ID: 4, LineTotalCents: 250}, // unassigned still counts toward the trip
	})

	if total != 1998 {
		t.Errorf("total = %d, want 1998", total)
	}
	if byStore[kroger] != 1248 {
		t.Errorf("kroger subtotal = %d, want 1248", byStore[kroger])
	}
	if byStore[aldi] != 500 {
		t.Errorf("aldi subtotal = %d, want 500", byStore[aldi])
	}
}

// An "I already have this" line stays on the list but is not being bought, so
// counting it would overstate the trip - in the total and in its store's
// subtotal alike.
func TestSummarizeLinesExcludesAlreadyHave(t *testing.T) {
	kroger := int64(7)
	total, byStore := summarizeLines([]*db.ShoppingListItem{
		{ID: 1, LineTotalCents: 899, StoreID: &kroger},
		{ID: 2, LineTotalCents: 1200, StoreID: &kroger, InPantry: true},
	})

	if total != 899 {
		t.Errorf("total = %d, want 899 - the already-have line was counted", total)
	}
	if byStore[kroger] != 899 {
		t.Errorf("kroger subtotal = %d, want 899", byStore[kroger])
	}
}

func TestSummarizeLinesEmpty(t *testing.T) {
	total, byStore := summarizeLines(nil)
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if byStore == nil {
		t.Error("byStore is nil; callers index it directly")
	}
}

// The have-it control and its shared dialog have to render, and an
// already-have line has to come out visibly different from a checked one -
// they mean different things (cupboard vs. cart).
func TestShoppingListBodyRendersHaveControl(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true,
			Tab:     "list",
			List: &shoppingListPageData{
				HasPlan:    true,
				TotalLabel: "$8.99",
				UnassignedItems: []shoppingLineItem{
					{ID: 1, DisplayName: "Ground beef", BuyQuantity: 2, Unit: "lb", TotalLabel: "$8.99"},
					{ID: 2, DisplayName: "Olive oil", BuyQuantity: 1, Unit: "bottle", TotalLabel: "$12.00", InPantry: true},
				},
			},
		},
	})

	for _, want := range []string{
		`have-btn no-print`,
		`data-qty="2"`,
		`data-unit="lb"`,
		// The already-have line is marked on both the button and the row.
		`have-btn--on`,
		`shopping-item--have`,
		`aria-pressed="true"`,
		// One dialog for the page, on the shared modal chrome.
		`id="have-it"`,
		`id="have-it-qty"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shopping list missing %q", want)
		}
	}
}

func TestLineLinkState(t *testing.T) {
	if got := lineLinkState(&db.Item{Name: "Chicken breast", Source: "builtin"}); got != "linked" {
		t.Errorf("a seeded catalog item = %q, want linked", got)
	}
	if got := lineLinkState(&db.Item{Name: "My thing", Source: "manual"}); got != "linked" {
		t.Errorf("a hand-created item = %q, want linked", got)
	}
	// An auto placeholder is the case the yellow chip exists for.
	if got := lineLinkState(&db.Item{Name: "chicken breasts", Source: "auto"}); got != "unmatched" {
		t.Errorf("an auto placeholder = %q, want unmatched", got)
	}
	if got := lineLinkState(nil); got != "unmatched" {
		t.Errorf("no item at all = %q, want unmatched", got)
	}
}

func TestBuildLineItemLinkState(t *testing.T) {
	real, auto := int64(1), int64(2)
	items := map[int64]*db.Item{
		real: {ID: real, Name: "Chicken breast", Source: "builtin"},
		auto: {ID: auto, Name: "chicken breasts", Source: "auto"},
	}

	green := buildLineItem(&db.ShoppingListItem{ID: 10, DisplayName: "Chicken breast", ItemID: &real}, nil, items, nil)
	if green.LinkState != "linked" {
		t.Errorf("LinkState = %q, want linked", green.LinkState)
	}
	if !strings.Contains(green.LinkLabel, "Chicken breast") {
		t.Errorf("LinkLabel = %q, want it to name the item", green.LinkLabel)
	}

	yellow := buildLineItem(&db.ShoppingListItem{ID: 11, DisplayName: "chicken breasts", ItemID: &auto}, nil, items, nil)
	if yellow.LinkState != "unmatched" {
		t.Errorf("LinkState = %q, want unmatched", yellow.LinkState)
	}

	// A line whose item id points at nothing must not panic or read as linked.
	missing := int64(999)
	orphan := buildLineItem(&db.ShoppingListItem{ID: 12, DisplayName: "mystery", ItemID: &missing}, nil, items, nil)
	if orphan.LinkState != "unmatched" {
		t.Errorf("dangling item id = %q, want unmatched", orphan.LinkState)
	}
}

func TestShoppingListBodyRendersLinkChips(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true,
			Tab:     "list",
			List: &shoppingListPageData{
				HasPlan:    true,
				TotalLabel: "$8.99",
				UnassignedItems: []shoppingLineItem{
					{ID: 1, DisplayName: "Chicken breast", LinkState: "linked", LinkLabel: "Linked to Chicken breast"},
					{ID: 2, DisplayName: "chicken breasts", LinkState: "unmatched", LinkLabel: "Not matched to a known item yet - click to pick one"},
				},
			},
		},
	})

	for _, want := range []string{
		`link-chip link-chip--linked`,
		`link-chip link-chip--unmatched`,
		`data-match-line`,
		`id="match-item"`,
		`id="match-item-list"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shopping list missing %q", want)
		}
	}
}

// Both the price editor and the HA setup panel were native <dialog
// class="modal"> elements with their own CSS and their own open/close calls.
// One convention means one place handling the focus trap, Escape, the backdrop
// and `inert` on <main>.
func TestShoppingListDialogsUseSharedChrome(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true, Tab: "list",
			List: &shoppingListPageData{HasPlan: true, TotalLabel: "$0.00"},
		},
	})

	for _, want := range []string{
		`class="modal-overlay" id="price-edit"`,
		`class="modal-overlay" id="ha-setup"`,
		`goeat.openModal('price-edit')`,
		`goeat.openModal('ha-setup')`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shopping list missing %q", want)
		}
	}
	for _, dead := range []string{"<dialog", "showModal()", "modal__title", "modal__actions", "modal__x"} {
		if strings.Contains(out, dead) {
			t.Errorf("a native <dialog> panel survives (%q)", dead)
		}
	}
}

func TestPantryNote(t *testing.T) {
	// Nothing on hand: no note at all.
	if got := pantryNote(&db.ShoppingListItem{PurchaseUnit: "lb"}); got != "" {
		t.Errorf("pantryNote with no deduction = %q, want empty", got)
	}

	// Partial: the note explains why the quantity shrank.
	got := pantryNote(&db.ShoppingListItem{PantryQtyUsed: 1, PurchaseUnit: "lb"})
	if got != "1 lb already in your pantry" {
		t.Errorf("partial note = %q", got)
	}

	// Fully covered reads differently: the line is not being bought, and
	// "2 lb already in your pantry" beside a quantity of zero makes the reader
	// do the subtraction themselves.
	got = pantryNote(&db.ShoppingListItem{PantryQtyUsed: 2, PurchaseUnit: "lb", InPantry: true})
	if !strings.HasPrefix(got, "all ") {
		t.Errorf("covered note = %q, want it to lead with 'all'", got)
	}
}

func TestShoppingListRendersPantryNote(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true, Tab: "list",
			List: &shoppingListPageData{
				HasPlan:    true,
				TotalLabel: "$8.99",
				UnassignedItems: []shoppingLineItem{
					{ID: 1, DisplayName: "Ground beef", BuyLabel: "2 lb", PantryNote: "1 lb already in your pantry"},
					{ID: 2, DisplayName: "Saffron", BuyLabel: "1 g"},
				},
			},
		},
	})

	if !strings.Contains(out, "1 lb already in your pantry") {
		t.Error("the pantry note is not rendered")
	}
	if !strings.Contains(out, "shopping-item__pantry-note") {
		t.Error("the pantry note has no class to style it")
	}
	// A line the pantry did not touch gets no note markup at all.
	if strings.Count(out, "shopping-item__pantry-note") != 1 {
		t.Errorf("expected exactly one pantry note, got %d", strings.Count(out, "shopping-item__pantry-note"))
	}
}

func TestShoppingListRendersPriceVerdict(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true, Tab: "list",
			List: &shoppingListPageData{
				HasPlan:    true,
				TotalLabel: "$8.99",
				UnassignedItems: []shoppingLineItem{
					{ID: 1, DisplayName: "Ground beef", PriceVerdict: "good price", PriceVerdictClass: "price-verdict--good"},
					{ID: 2, DisplayName: "Saffron"},
				},
			},
		},
	})

	if !strings.Contains(out, "good price") {
		t.Error("the price verdict is not rendered")
	}
	// An ordinary price renders nothing - silence is the common case.
	if strings.Count(out, `class="price-verdict `) != 1 {
		t.Errorf("expected exactly one verdict, got %d", strings.Count(out, `class="price-verdict `))
	}
}
