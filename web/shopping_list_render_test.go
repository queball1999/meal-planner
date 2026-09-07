package web

import (
	"strings"
	"testing"
)

func TestMealColorClass_StableAndInRange(t *testing.T) {
	for _, title := range []string{"Chili", "Tacos", "Oatmeal with berries", ""} {
		c1 := mealColorClass(title)
		c2 := mealColorClass(title)
		if c1 != c2 {
			t.Fatalf("mealColorClass(%q) not stable: %q vs %q", title, c1, c2)
		}
		if !strings.HasPrefix(c1, "badge-meal-") {
			t.Fatalf("mealColorClass(%q) = %q, want badge-meal-N", title, c1)
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
									{Title: "Chili", ColorClass: "badge-meal-3"},
									{Title: "Taco Bowls", ColorClass: "badge-meal-6"},
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
		`badge-meal-3`,
		`Chili`,
		`badge-meal-6`,
		`Taco Bowls`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}
