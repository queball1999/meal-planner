package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"goeat/db"
)

// "Correct meal plan" on a week whose only fault is a dinner not cooking
// enough for the next day's leftovers: fixed without a model (the fixture has
// none), reported as a step, and the turn saved to the conversation.
func TestChatReviewCooksEnoughForLeftovers(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()

	// A week far enough off that none of it has "already happened".
	p, err := f.store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: f.a.hh.ID, WeekStart: "2099-01-04", WeekEnd: "2099-01-10", BudgetCents: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdatePlanStatus(ctx, p.ID, "ready"); err != nil {
		t.Fatal(err)
	}
	// Every slot filled, so nothing is left that needs a dish chosen.
	var chili, left *db.Meal
	for _, day := range []string{"2099-01-04", "2099-01-05", "2099-01-06", "2099-01-07", "2099-01-08", "2099-01-09", "2099-01-10"} {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			title := "Meal " + day + " " + slot
			switch {
			case day == "2099-01-04" && slot == "dinner":
				title = "Chili"
			case day == "2099-01-05" && slot == "lunch":
				title = "Leftover Chili"
			}
			m, err := f.store.CreateMeal(ctx, db.CreateMealParams{
				PlanID: p.ID, Day: day, Slot: slot, Title: title, Effort: "quick", Servings: 2, CookedPortions: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			switch title {
			case "Chili":
				chili = m
			case "Leftover Chili":
				left = m
			}
		}
	}
	if err := f.store.UpdateMealLeftover(ctx, left.ID, true, &chili.ID); err != nil {
		t.Fatal(err)
	}

	w := f.do("alice", "POST", "/chat/review", url.Values{"plan_id": {strconv.FormatInt(p.ID, 10)}})
	body := w.Body.String()
	for _, want := range []string{"event: step", `"tool":"cook_more"`, "event: done", "cooks enough for every leftover"} {
		if !strings.Contains(body, want) {
			t.Errorf("response is missing %q:\n%s", want, body)
		}
	}
	if m, _ := f.store.GetMealByID(ctx, chili.ID); m.CookedPortions != 4 {
		t.Errorf("chili cooks %d portions, want 4", m.CookedPortions)
	}
	msgs, _ := f.store.ListChatMessages(ctx, f.a.hh.ID, 10)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("conversation = %+v, want the request and the reply", msgs)
	}

	// A second press finds nothing, and says so rather than changing anything.
	body = f.do("alice", "POST", "/chat/review", url.Values{"plan_id": {strconv.FormatInt(p.ID, 10)}}).Body.String()
	if !strings.Contains(body, "Nothing to correct") || strings.Contains(body, "event: step") {
		t.Errorf("second review should be clean:\n%s", body)
	}
}

// "Correct meal plan" on a week whose lunches were never planned: each one
// becomes the leftovers of the dinner before it, without a model.
func TestChatReviewFillsLunchesWithLeftovers(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()

	p, err := f.store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: f.a.hh.ID, WeekStart: "2099-01-04", WeekEnd: "2099-01-10", BudgetCents: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdatePlanStatus(ctx, p.ID, "ready"); err != nil {
		t.Fatal(err)
	}
	days := []string{"2099-01-04", "2099-01-05", "2099-01-06", "2099-01-07", "2099-01-08", "2099-01-09", "2099-01-10"}
	for i, day := range days {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			// The first day's lunch is planned; no other lunch is.
			if slot == "lunch" && i > 0 {
				continue
			}
			if _, err := f.store.CreateMeal(ctx, db.CreateMealParams{
				PlanID: p.ID, Day: day, Slot: slot, Title: "Meal " + day + " " + slot, Effort: "quick", Servings: 2, CookedPortions: 2,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	body := f.do("alice", "POST", "/chat/review", url.Values{"plan_id": {strconv.FormatInt(p.ID, 10)}}).Body.String()
	for _, want := range []string{`"tool":"use_leftovers"`, "event: done", "6 lunches are now the leftovers"} {
		if !strings.Contains(body, want) {
			t.Errorf("response is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Still not right") {
		t.Errorf("review still finds problems:\n%s", body)
	}
	meals, _ := f.store.ListMealsByPlan(ctx, p.ID)
	leftovers := 0
	for _, m := range meals {
		if m.Slot == "lunch" && m.IsLeftover && m.LeftoverSourceMealID != nil {
			leftovers++
		}
	}
	if len(meals) != 21 || leftovers != 6 {
		t.Errorf("plan has %d meals and %d leftover lunches, want 21 and 6", len(meals), leftovers)
	}
}
