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
	// Ingredient rows 1 and 3 both belong to meal 100 (the same batch-cooked
	// chili used in two ingredient lines) - the dedupe key is the meal id, not
	// the ingredient ref, so both must collapse into one tag.
	refs := map[int64]db.MealRef{
		1: {MealID: 100, Title: "Chili"},
		2: {MealID: 200, Title: "Tacos"},
		3: {MealID: 100, Title: "Chili"},
	}
	tags := mealTagsFor(`[1,2,3]`, refs)
	if len(tags) != 2 {
		t.Fatalf("got %d tags, want 2 (deduped): %+v", len(tags), tags)
	}
	if tags[0].Title != "Chili" || tags[0].MealID != 100 {
		t.Fatalf("tags[0] = %+v, want Chili/100", tags[0])
	}
	if tags[1].Title != "Tacos" || tags[1].MealID != 200 {
		t.Fatalf("tags[1] = %+v, want Tacos/200", tags[1])
	}
	if tags[0].ColorClass == "" {
		t.Fatal("expected a non-empty color class")
	}
}

func TestMealTagsFor_EmptyInputs(t *testing.T) {
	if tags := mealTagsFor("", map[int64]db.MealRef{1: {MealID: 100, Title: "Chili"}}); tags != nil {
		t.Fatalf("expected nil for empty refs JSON, got %+v", tags)
	}
	if tags := mealTagsFor(`[1]`, nil); tags != nil {
		t.Fatalf("expected nil for empty title map, got %+v", tags)
	}
	if tags := mealTagsFor(`not json`, map[int64]db.MealRef{1: {MealID: 100, Title: "Chili"}}); tags != nil {
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
									{Title: "Chili", ColorClass: "badge-hue-3", MealID: 42},
									{Title: "Taco Bowls", ColorClass: "badge-hue-6", MealID: 43},
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
		// Each pill is a hovercard.js trigger for the real meal (not the
		// ingredient row it came from) - see mealTagsFor.
		`data-meal-id="42"`,
		`data-meal-id="43"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

// A line CostPlan has seeded but not yet priced (SeedShoppingList) renders as
// a loading placeholder, not a real (and wrong) $0.00 price, and the page
// shows the "still pricing" banner that drives the poll loop.
func TestShoppingListBody_RendersPendingRowsAsSkeleton(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true,
			Tab:     "list",
			List: &shoppingListPageData{
				HasPlan:     true,
				TotalLabel:  "$0.00",
				BudgetLabel: "$50",
				Pricing:     true,
				Groups: []shoppingStoreGroup{
					{
						StoreName:     "Kroger",
						SubtotalLabel: "$0.00",
						Items: []shoppingLineItem{
							{ID: 1, DisplayName: "canned black beans", Pending: true},
						},
					},
				},
			},
		},
	})
	for _, want := range []string{
		`id="shopping-list-live" data-pricing="1"`,
		`shopping-pricing-note`,
		`shopping-item--pending`,
		`skel-bar`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// A pending row must not show a real price-edit control - editing it
	// would race CostPlan's own resolve pass overwriting the same row.
	if strings.Contains(out, `class="btn btn-ghost btn-icon btn-sm price-edit-btn`) {
		t.Error("a pending row rendered a live price-edit button")
	}
}

// Once every line is priced, the "still pricing" banner and skeleton markup
// disappear and the poll loop's data-pricing flag flips to 0 - see
// pollPricing in shopping_list_body.html, which stops polling on this.
func TestShoppingListBody_NoPricingBannerWhenResolved(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "list",
		Data: planPageData{
			HasPlan: true,
			Tab:     "list",
			List: &shoppingListPageData{
				HasPlan:     true,
				TotalLabel:  "$1.29",
				BudgetLabel: "$50",
				Pricing:     false,
				Groups: []shoppingStoreGroup{
					{
						StoreName:     "Kroger",
						SubtotalLabel: "$1.29",
						Items: []shoppingLineItem{
							{ID: 1, DisplayName: "canned black beans", BuyLabel: "1 can", TotalLabel: "$1.29", BadgeClass: "badge-estimate", BadgeText: "Estimated"},
						},
					},
				},
			},
		},
	})
	if !strings.Contains(out, `id="shopping-list-live" data-pricing="0"`) {
		t.Error("data-pricing should be 0 once every line is resolved")
	}
	if strings.Contains(out, `shopping-pricing-note`) {
		t.Error("the still-pricing banner rendered with nothing pending")
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

	green := buildLineItem(&db.ShoppingListItem{ID: 10, DisplayName: "Chicken breast", ItemID: &real}, nil, items, nil, "")
	if green.LinkState != "linked" {
		t.Errorf("LinkState = %q, want linked", green.LinkState)
	}
	if !strings.Contains(green.LinkLabel, "Chicken breast") {
		t.Errorf("LinkLabel = %q, want it to name the item", green.LinkLabel)
	}

	yellow := buildLineItem(&db.ShoppingListItem{ID: 11, DisplayName: "chicken breasts", ItemID: &auto}, nil, items, nil, "")
	if yellow.LinkState != "unmatched" {
		t.Errorf("LinkState = %q, want unmatched", yellow.LinkState)
	}

	// A line whose item id points at nothing must not panic or read as linked.
	missing := int64(999)
	orphan := buildLineItem(&db.ShoppingListItem{ID: 12, DisplayName: "mystery", ItemID: &missing}, nil, items, nil, "")
	if orphan.LinkState != "unmatched" {
		t.Errorf("dangling item id = %q, want unmatched", orphan.LinkState)
	}
}

func TestBuildLineItemUnitSystem(t *testing.T) {
	id := int64(1)
	items := map[int64]*db.Item{id: {ID: id, Name: "Flour", StockUnit: "g"}}
	line := &db.ShoppingListItem{ID: 5, DisplayName: "Flour", ItemID: &id, BuyQuantity: 1200, PurchaseUnit: "bag"}

	asIs := buildLineItem(line, nil, items, nil, "")
	if asIs.BuyLabel != "1200 g" {
		t.Errorf("as-is BuyLabel = %q, want %q", asIs.BuyLabel, "1200 g")
	}

	metric := buildLineItem(line, nil, items, nil, "metric")
	if metric.BuyLabel != "1.2 kg" {
		t.Errorf("metric BuyLabel = %q, want %q", metric.BuyLabel, "1.2 kg")
	}

	imperial := buildLineItem(line, nil, items, nil, "imperial")
	if imperial.BuyLabel != "2.65 lb" {
		t.Errorf("imperial BuyLabel = %q, want %q", imperial.BuyLabel, "2.65 lb")
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

func TestQuickConversions_WeightFamily(t *testing.T) {
	rows := quickConversions(2, "lb", nil)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Label] = true
	}
	// 2 lb = 32 oz, ~907 g, ~0.91 kg - the other three members of the family.
	if len(rows) != 3 {
		t.Fatalf("got %d conversions, want 3: %+v", len(rows), rows)
	}
	if !got["32 oz"] {
		t.Errorf("missing oz conversion: %+v", rows)
	}
}

func TestQuickConversions_VolumeFamily(t *testing.T) {
	rows := quickConversions(1, "cup", nil)
	if len(rows) == 0 {
		t.Fatal("expected volume-family conversions for cup, got none")
	}
	for _, r := range rows {
		if r.Label == "" {
			t.Errorf("empty conversion label: %+v", rows)
		}
	}
}

// A count-like unit (each, package, ...) has no family - nothing sensible to
// convert "3 each" into, so the popup should have nothing to show rather than
// a made-up number.
func TestQuickConversions_NoFamilyForCountUnits(t *testing.T) {
	if rows := quickConversions(3, "each", nil); rows != nil {
		t.Errorf("expected nil for a count unit, got %+v", rows)
	}
	if rows := quickConversions(2, "package", nil); rows != nil {
		t.Errorf("expected nil for a count unit, got %+v", rows)
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
