package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"goeat/db"
	"goeat/pricing"
)

// "Regenerate from meal plan" drops a line whose ingredient no meal uses any
// more and adds one the list was missing; a viewer cannot run it.
func TestPlanListRebuild(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()
	planID := f.a.meal.PlanID

	// The fixture's plan is for a week already over, which is read only.
	stale := f.do("alice", "POST", "/plan/list/rebuild", nil)
	if stale.Code != http.StatusForbidden && stale.Code != http.StatusNotFound {
		t.Fatalf("past week: %d %s, want it refused", stale.Code, stale.Body.String())
	}

	p, err := f.store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: f.a.hh.ID, WeekStart: "2099-01-04", WeekEnd: "2099-01-10", BudgetCents: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdatePlanStatus(ctx, p.ID, "ready"); err != nil {
		t.Fatal(err)
	}
	planID = p.ID
	m, err := f.store.CreateMeal(ctx, db.CreateMealParams{PlanID: planID, Day: "2099-01-05", Slot: "dinner", Title: "Sloppy Joes", Effort: "quick", Servings: 2, CookedPortions: 2})
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string) {
		t.Helper()
		if err := f.store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID: m.ID, Name: name, Quantity: 1, Unit: "each", NormalizedTerm: pricing.Normalize(name),
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("ground beef")
	add("worcestershire sauce")
	if _, err := pricing.EnsureShoppingList(ctx, f.store, planID, f.a.hh); err != nil {
		t.Fatal(err)
	}

	// The meal changes under the list, with nothing telling the list.
	if err := f.store.DeleteMealIngredients(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	add("ground beef")
	add("hamburger buns")

	const week = "/plan/list/rebuild?week=2099-01-04"
	if w := f.do("vera", "POST", week, nil); w.Code != http.StatusForbidden {
		t.Errorf("viewer: %d, want 403", w.Code)
	}
	w := f.do("alice", "POST", week, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("rebuild: %d %s", w.Code, w.Body.String())
	}
	for i := 0; f.srv.isRepricing(planID); i++ {
		if i > 500 {
			t.Fatal("rebuild did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	rows, _ := f.store.ListShoppingListItems(ctx, planID)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.DisplayName] = true
	}
	if len(rows) != 2 || !got["ground beef"] || !got["hamburger buns"] {
		t.Errorf("shopping list = %v, want ground beef and hamburger buns", got)
	}
}
