package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"goeat/db"
)

// TestReconcilePlanStatus_CancelsOtherPlansForWeek covers the crash-recovery
// path: a plan left stuck at "generating" (e.g. the process restarted mid-run,
// after meals were persisted but before plan.Generate reached its final
// UpdatePlanStatus/CancelOtherPlansForWeek calls) must still retire any other
// active plan for the same week once reconcilePlanStatus repairs it to
// "ready" - otherwise two "ready" plans stack up for one week and
// GetLatestPlan/GetPlanByWeekStart's "one active plan per week" invariant
// breaks.
// TestCanRegeneratePlan covers the gate behind /plan/history's retry button:
// it must line up exactly with what handlePlanGenerate itself would accept,
// or the button would either offer a retry that then gets refused, or hide
// one that would have worked.
func TestCanRegeneratePlan(t *testing.T) {
	curStart := time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC) // a Monday, "today"'s week

	cases := []struct {
		name string
		p    *db.Plan
		want bool
	}{
		{"failed current week", &db.Plan{Status: "error", WeekStart: "2026-01-12"}, true},
		{"failed future week", &db.Plan{Status: "error", WeekStart: "2026-01-19"}, true},
		{"failed past week - can't regenerate", &db.Plan{Status: "error", WeekStart: "2026-01-05"}, false},
		{"failed but superseded/canceled", &db.Plan{Status: "error", WeekStart: "2026-01-12", Canceled: true}, false},
		{"ready plan - nothing to retry", &db.Plan{Status: "ready", WeekStart: "2026-01-12"}, false},
		{"still generating", &db.Plan{Status: "generating", WeekStart: "2026-01-12"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canRegeneratePlan(c.p, curStart); got != c.want {
				t.Errorf("canRegeneratePlan() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPlanIsMidWeekRetry covers the gate behind offering the must-include
// modal's "whole week or just the remaining days" choice on a retry: it must
// only fire for the live week, and only when today isn't that week's first
// day - a future week's retry (nothing has happened yet) or a "today is the
// first day" retry (nothing to skip) should just replan normally.
func TestPlanIsMidWeekRetry(t *testing.T) {
	curStart := time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		weekStart    string
		todayMidWeek bool
		want         bool
	}{
		{"live week, mid-week", "2026-01-12", true, true},
		{"live week, but today is the first day", "2026-01-12", false, false},
		{"future week - never mid-week", "2026-01-19", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &db.Plan{WeekStart: c.weekStart}
			if got := planIsMidWeekRetry(p, curStart, c.todayMidWeek); got != c.want {
				t.Errorf("planIsMidWeekRetry() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReconcilePlanStatus_CancelsOtherPlansForWeek(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}

	// Plan A: a normal, already-ready plan for the week (as if from an
	// earlier successful generation).
	planA, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create plan A: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, planA.ID, "ready"); err != nil {
		t.Fatalf("mark A ready: %v", err)
	}

	// Plan B: a regenerate for the same week that crashed after persisting
	// meals but before its own status/cancel update ran - stuck at "generating"
	// with no job in flight for it.
	planB, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create plan B: %v", err)
	}
	meal, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: planB.ID, Day: "2026-01-05", Slot: "dinner", Title: "Chili", Effort: "quick", Servings: 2, CookedPortions: 2})
	if err != nil {
		t.Fatalf("create meal: %v", err)
	}
	_ = meal

	s := &Server{store: store} // jobs left nil: reconcilePlanStatus treats that as "no job running"

	status := s.reconcilePlanStatus(ctx, hh.ID, planB)
	if status != "ready" {
		t.Fatalf("reconciled status = %q, want ready", status)
	}

	refreshedA, err := store.GetPlanByID(ctx, planA.ID)
	if err != nil || refreshedA == nil {
		t.Fatalf("get plan A: %v", err)
	}
	if !refreshedA.Canceled {
		t.Fatal("plan A should be canceled once plan B is reconciled to ready")
	}

	latest, err := store.GetLatestPlan(ctx, hh.ID)
	if err != nil || latest == nil || latest.ID != planB.ID {
		t.Fatalf("latest plan = %+v, want plan B (id %d)", latest, planB.ID)
	}
}

// TestBuildWeekNav_TodayCarriesExplicitWeekParam is the "clicking Today lands
// a week in the past" regression: when the plan page has no ?week= param it
// falls back to whichever plan GetLatestPlan resolves to, which can be a
// stale week (e.g. the most recently *generated* plan, not the current one).
// A bare link back to the tab's base path would silently re-resolve to that
// same stale week - the "today" link has to carry its own explicit ?week= so
// it actually lands on today's week regardless of what else is showing.
func TestBuildWeekNav_TodayCarriesExplicitWeekParam(t *testing.T) {
	viewed := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC) // a stale plan's week
	today := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC) // the actual current week

	nav := buildWeekNav(viewed, today, "/plan")

	want := "/plan?week=2026-09-13"
	if nav.today != want {
		t.Fatalf("today link = %q, want %q (today's own week, not the viewed/stale one)", nav.today, want)
	}
	if nav.today == "/plan" {
		t.Fatalf("today link has no ?week= param - it would fall back to whatever plan is latest, not necessarily today's week")
	}
}

// TestBuildRequestedMeals covers the generate form's "meals you want this
// week" inputs: a picked recipe has to come back with its ingredients so the
// LLM reproduces it rather than reinventing something similar, free-typed
// lines pass through as-is, blank lines are dropped, and a recipe_id from
// another household (or garbage) is silently skipped rather than leaking
// someone else's recipe into the prompt.
func TestBuildRequestedMeals(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	other, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "Other"})
	if err != nil {
		t.Fatalf("other household: %v", err)
	}

	recipe, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: hh.ID, Title: "Grandma's Lasagna", SourceKind: "manual", Servings: 4,
	})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	if err := store.AddCatalogRecipeIngredient(ctx, recipe.ID, "lasagna noodles", "1", "box", 0); err != nil {
		t.Fatalf("add ingredient: %v", err)
	}

	foreignRecipe, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: other.ID, Title: "Someone Else's Secret Recipe", SourceKind: "manual", Servings: 2,
	})
	if err != nil {
		t.Fatalf("create foreign recipe: %v", err)
	}

	s := &Server{store: store}

	form := url.Values{}
	form.Set("recipe_ids", strconv.FormatInt(recipe.ID, 10))
	form.Add("recipe_ids", strconv.FormatInt(foreignRecipe.ID, 10)) // not this household - must be skipped
	form.Add("recipe_ids", "not-a-number")                          // garbage - must be skipped
	form.Set("must_include_text", "something with salmon on Friday\n\n  \nanother request")

	r, err := http.NewRequest(http.MethodPost, "/plan/generate", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	out := s.buildRequestedMeals(r, hh.ID)

	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "Grandma's Lasagna") {
		t.Errorf("picked recipe missing from result:\n%v", out)
	}
	if !strings.Contains(joined, "lasagna noodles") {
		t.Errorf("recipe's ingredients missing from result:\n%v", out)
	}
	if strings.Contains(joined, "Someone Else's Secret Recipe") {
		t.Errorf("a recipe from another household leaked into the result:\n%v", out)
	}
	if !strings.Contains(joined, "something with salmon on Friday") {
		t.Errorf("free-typed line missing:\n%v", out)
	}
	if !strings.Contains(joined, "another request") {
		t.Errorf("second free-typed line missing:\n%v", out)
	}
	// Exactly one entry per picked recipe/typed line, plus no blank entries
	// from the blank line in must_include_text.
	if len(out) != 3 {
		t.Fatalf("got %d requested-meal entries, want 3 (1 recipe + 2 typed lines):\n%v", len(out), out)
	}
}
