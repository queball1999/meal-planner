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
