package web

import (
	"context"
	"strings"
	"testing"

	"goeat/db"
)

func TestShoppingLineRefs(t *testing.T) {
	got := shoppingLineRefs(`[1,2,3]`)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("refs = %v, want [1 2 3]", got)
	}
	// A line that cannot be attributed is skipped, not an error.
	for _, bad := range []string{"", "not json", "{}"} {
		if r := shoppingLineRefs(bad); r != nil {
			t.Errorf("shoppingLineRefs(%q) = %v, want nil", bad, r)
		}
	}
}

// A shopping line is shared between every meal that uses the ingredient, so a
// meal claiming the whole line would double-count. The line is split across
// the meals referencing it.
func TestMealCostSplitsSharedLines(t *testing.T) {
	srv := &Server{store: &costStub{
		lines: []*db.ShoppingListItem{
			// Only this meal's ingredient (1): the whole line.
			{ID: 10, LineTotalCents: 400, MealIngredientRefs: `[1]`},
			// Shared with another meal's ingredient (99): half.
			{ID: 11, LineTotalCents: 600, MealIngredientRefs: `[2,99]`},
			// Another meal entirely: nothing.
			{ID: 12, LineTotalCents: 900, MealIngredientRefs: `[99]`},
			// Already owned, so not part of what this trip costs.
			{ID: 13, LineTotalCents: 500, MealIngredientRefs: `[1]`, InPantry: true},
		},
	}}

	meal := &db.Meal{ID: 5, PlanID: 1}
	ings := []*db.MealIngredient{{ID: 1}, {ID: 2}}

	got := srv.mealCostLabel(t.Context(), meal, ings)
	if got != "about $7.00" {
		t.Errorf("cost = %q, want about $7.00 (400 + 600/2)", got)
	}
}

func TestMealCostEmptyWhenNothingPriced(t *testing.T) {
	srv := &Server{store: &costStub{lines: []*db.ShoppingListItem{
		{ID: 1, LineTotalCents: 0, MealIngredientRefs: `[1]`},
	}}}
	if got := srv.mealCostLabel(t.Context(), &db.Meal{PlanID: 1}, []*db.MealIngredient{{ID: 1}}); got != "" {
		t.Errorf("cost = %q, want empty - an unpriced meal must not claim $0.00", got)
	}
	// A meal with no ingredients has nothing to attribute.
	if got := srv.mealCostLabel(t.Context(), &db.Meal{PlanID: 1}, nil); got != "" {
		t.Errorf("cost = %q, want empty", got)
	}
}

// The dashboard's "This week spent" used badge--success/--danger, which paint
// a tinted block behind the text inside a chip that already has a surface.
func TestDashboardStatsChipHasNoTextBackground(t *testing.T) {
	out := renderPage(t, "index", pageData{
		AppName: "Go Eat",
		Page:    "index",
		User:    &db.User{Username: "sam"},
		Data: dashPageData{
			StatsSpent: "$82.10",
			StatsMeals: 21,
		},
	})

	if strings.Contains(out, "badge--success") || strings.Contains(out, "badge--danger") {
		t.Error("the stats chip still paints a background behind its value")
	}
	if !strings.Contains(out, "stats-chip__value--under") {
		t.Error("the under-budget colour class is missing")
	}
}

// Meals on the dashboard calendar carry the hover-card trigger.
func TestDashboardMealsCarryHoverTrigger(t *testing.T) {
	out := renderPage(t, "index", pageData{
		AppName: "Go Eat",
		Page:    "index",
		User:    &db.User{Username: "sam"},
		Data: dashPageData{
			Calendar: dashCalendar{
				Mode:  "week",
				Title: "Jan 4 - Jan 10",
				Rows: [][]calDayCell{{{
					Date: "2026-01-05", DayNum: 5, Weekday: "Mon", InScope: true,
					Meals: []calMeal{{MealID: 7, Slot: "dinner", Title: "Chili"}},
				}}},
			},
		},
	})

	if !strings.Contains(out, `data-meal-id="7"`) {
		t.Error("calendar meal has no hover-card trigger")
	}
	if !strings.Contains(out, "/static/js/hovercard.js") {
		t.Error("hovercard.js is not loaded")
	}
}

// The dashboard's plan-history card shares the same row-click detail modal
// and retry-on-failure button as the full /plan/history page - this must not
// silently regress back to a plain, non-clickable table.
func TestDashboardPlanHistoryCardHasDetailAndRetry(t *testing.T) {
	out := renderPage(t, "index", pageData{
		AppName: "Go Eat",
		Page:    "index",
		User:    &db.User{Username: "sam"},
		Data: dashPageData{
			HasLLM: true,
			RecentPlans: []historyPlanRow{
				{Plan: &db.Plan{ID: 9, WeekStart: "2026-01-12", WeekEnd: "2026-01-18", Status: "error", BudgetCents: 10000}, CanRegenerate: true},
			},
		},
	})

	if !strings.Contains(out, `data-history-detail="9"`) {
		t.Error("dashboard plan-history row is not wired for the detail modal")
	}
	if !strings.Contains(out, "Retry generating this plan") {
		t.Error("dashboard plan-history row does not offer a retry for a regeneratable failed plan")
	}
	// The retry action must go through must-include-modal (same picker and
	// confirm dialog as every other regenerate button), not post straight to
	// /plan/generate.
	if !strings.Contains(out, `data-modal-open="must-include-modal"`) {
		t.Error("dashboard retry button does not open the must-include-modal picker/confirm flow")
	}
	if !strings.Contains(out, `id="history-detail-modal"`) {
		t.Error("dashboard is missing the shared history-detail modal")
	}
}

// costStub answers the one query mealCostLabel makes. db.Store is embedded as
// a nil interface rather than implemented: the interface has well over a
// hundred methods, and a stub that spells out all of them would need editing
// every time an unrelated one is added. Any method this test does not expect
// to be called panics, which is the behaviour we want if that ever changes.
type costStub struct {
	db.Store
	lines []*db.ShoppingListItem
}

func (c *costStub) ListShoppingListItems(context.Context, int64) ([]*db.ShoppingListItem, error) {
	return c.lines, nil
}
