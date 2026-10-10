package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"goeat/config"
	"goeat/db"
)

// The dialog's date preview and the submit both go through
// resolveGenerateTarget, so this is the whole of "which days get planned".
func TestResolveGenerateTarget(t *testing.T) {
	s := &Server{cfg: &config.Config{WeekStartDay: "sunday"}}
	at := func(day string) time.Time {
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Add(15 * time.Hour)
	}
	cases := []struct {
		name, now, week, scope string
		wantWeek, wantFrom     string
		rolled, midWeek        bool
	}{
		{"no week, mid-week, whole week", "2026-10-07", "", "full", "2026-10-04", "2026-10-04", false, true},
		{"no week, mid-week, remaining days", "2026-10-07", "", "remaining", "2026-10-04", "2026-10-07", false, true},
		{"no week, first day", "2026-10-04", "", "remaining", "2026-10-04", "2026-10-04", false, false},
		// The day before the week starts: next week, all of it.
		{"no week, last day rolls to next week", "2026-10-10", "", "remaining", "2026-10-11", "2026-10-11", true, false},
		// ...unless this week was asked for by name, which is then just today.
		{"this week named on its last day", "2026-10-10", "2026-10-04", "remaining", "2026-10-04", "2026-10-10", false, true},
		{"a day inside a future week", "2026-10-07", "2026-10-14", "remaining", "2026-10-11", "2026-10-11", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.resolveGenerateTarget(c.week, c.scope, at(c.now))
			if err != nil {
				t.Fatalf("resolveGenerateTarget: %v", err)
			}
			if w, f := got.WeekStart.Format("2006-01-02"), got.FromDate.Format("2006-01-02"); w != c.wantWeek || f != c.wantFrom ||
				got.Rolled != c.rolled || got.MidWeek != c.midWeek {
				t.Errorf("got week %s from %s rolled=%v midWeek=%v, want week %s from %s rolled=%v midWeek=%v",
					w, f, got.Rolled, got.MidWeek, c.wantWeek, c.wantFrom, c.rolled, c.midWeek)
			}
		})
	}

	if _, err := s.resolveGenerateTarget("2026-09-30", "full", at("2026-10-07")); !errors.Is(err, errGeneratePastWeek) {
		t.Errorf("a week that has ended: err = %v, want errGeneratePastWeek", err)
	}
	if _, err := s.resolveGenerateTarget("next tuesday", "full", at("2026-10-07")); !errors.Is(err, errGenerateBadWeek) {
		t.Errorf("an unparseable week: err = %v, want errGenerateBadWeek", err)
	}
}

// An edit to a day belongs to the plan covering that day, not to whichever
// plan was created last.
func TestPlanForDate(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T", HouseholdSize: 2})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	s := &Server{cfg: &config.Config{WeekStartDay: "sunday"}, store: store}

	thisWeek, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-10-04", WeekEnd: "2026-10-10"})
	if err != nil {
		t.Fatal(err)
	}
	// Planned ahead, so it is the newest plan.
	nextWeek, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-10-11", WeekEnd: "2026-10-17"})
	if err != nil {
		t.Fatal(err)
	}

	if p := s.planForDate(ctx, hh, "2026-10-08"); p == nil || p.ID != thisWeek.ID {
		t.Errorf("planForDate(this week) = %+v, want plan %d", p, thisWeek.ID)
	}
	if p := s.planForDate(ctx, hh, "2026-10-13"); p == nil || p.ID != nextWeek.ID {
		t.Errorf("planForDate(next week) = %+v, want plan %d", p, nextWeek.ID)
	}
	if p := s.planForDate(ctx, hh, "2026-11-20"); p != nil {
		t.Errorf("planForDate(unplanned week) = plan %d, want none", p.ID)
	}
	if p := s.planForDate(ctx, hh, "soon"); p != nil {
		t.Errorf("planForDate(bad date) = plan %d, want none", p.ID)
	}
}

// An editable board can be rearranged: every slot takes a drop, an unlocked
// meal can be picked up or moved from its menu, and the dialogs that settle a
// move are on the page. A read-only board offers none of it.
func TestPlanBoardOffersMoves(t *testing.T) {
	days := []calendarDay{{
		Date: "2026-01-05", DateLabel: "Mon Jan 5", Headcount: 2, Status: db.DayCooking,
		Slots: map[string]calendarSlot{
			"breakfast": {IsEmpty: true},
			"lunch":     {Title: "Soup", MealID: 4, Locked: true},
			"dinner":    {Title: "Chili", MealID: 3},
		},
	}}
	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, false)})
	for _, want := range []string{
		`data-drop-date="2026-01-05" data-drop-slot="breakfast"`,
		`draggable="true" data-drag-meal="3"`,
		`data-move-meal`,
		`data-remove-meal`,
		`id="move-meal"`,
		`id="move-meal-form"`,
		`id="pick-leftovers-list"`,
		`name="leftover_of"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("editable plan page missing %q", want)
		}
	}
	// A locked meal stays where it is.
	if strings.Contains(out, `data-drag-meal="4"`) {
		t.Error("a locked meal was made draggable")
	}

	out = renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, true)})
	for _, unwanted := range []string{`draggable="true"`, `data-drop-date`, `data-move-meal`, `data-remove-meal`, `id="move-meal"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("read-only plan page has %q", unwanted)
		}
	}
}

// A week with no plan offers building one by hand - with or without an LLM -
// for the week on screen, and offers nothing for a week that has ended.
func TestPlanEmptyStateOffersManual(t *testing.T) {
	data := planPageData{Tab: "plan", ViewedWeek: "2026-10-11", CanPlanWeek: true, WeekNavTitle: "Oct 11 – Oct 17"}
	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", CanEdit: true, Data: data})
	for _, want := range []string{`action="/plan/manual"`, `name="week" value="2026-10-11"`, `Plan it myself`} {
		if !strings.Contains(out, want) {
			t.Errorf("empty plan page missing %q", want)
		}
	}
	if strings.Contains(out, `data-modal-open="must-include-modal"`) {
		t.Error("the AI generate button was offered with no LLM configured")
	}

	data.HasLLM = true
	out = renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", CanEdit: true, Data: data})
	if !strings.Contains(out, `data-modal-open="must-include-modal" data-week="2026-10-11"`) {
		t.Error("the generate button does not name the week on screen")
	}

	data.CanPlanWeek = false
	out = renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", CanEdit: true, Data: data})
	// By label: with an LLM the generate dialog's own markup is on the page
	// either way, and it mentions its trigger selector.
	if strings.Contains(out, `/plan/manual`) || strings.Contains(out, `Generate a plan`) {
		t.Error("a week that has ended was offered planning")
	}
}

// What the generate dialog submitted is kept on the plan it produced, and
// comes back through the list the dialog reuses requests from.
func TestGenerateSavesRequest(t *testing.T) {
	s, hh := newSchedulerTestServer(t, -1)
	ctx := context.Background()
	recipe, err := s.store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: hh.ID, Title: "Lasagna", SourceKind: "manual", Servings: 4,
	})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	week := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	req := &db.PlanRequest{
		Recipes: []db.PlanRequestRecipe{{ID: recipe.ID, Title: "Lasagna"}, {ID: recipe.ID + 100, Title: "Deleted since"}},
		Text:    "salmon on Friday",
		OnHand:  []db.PlanRequestOnHand{{Name: "Rice", Quantity: 2, Unit: "kg"}},
		Scope:   "full",
	}

	job, started := s.startPlanGenerationForWeek(hh.ID, week, week, []string{"Lasagna", "salmon on Friday"}, []string{"Rice (2 kg)"}, req)
	if !started {
		t.Fatal("generation did not start")
	}
	select {
	case <-job.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("generation did not finish")
	}

	p, err := s.store.GetPlanByWeekStart(ctx, hh.ID, "2026-10-11")
	if err != nil || p == nil {
		t.Fatalf("plan for the week: %+v err %v", p, err)
	}
	v := s.planRequestViewFor(ctx, hh.ID, p)
	if v == nil {
		t.Fatal("the generated plan has no saved request")
	}
	if v.Text != "salmon on Friday" || len(v.OnHand) != 1 || v.OnHand[0].Name != "Rice" {
		t.Errorf("saved request = %+v, want the text and on-hand rows as submitted", v)
	}
	if len(v.Recipes) != 2 || v.Recipes[0].Missing || !v.Recipes[1].Missing {
		t.Errorf("recipes = %+v, want Lasagna present and the other flagged missing", v.Recipes)
	}
	if v.Summary != "2 recipes, 1 typed ask, 1 on hand" {
		t.Errorf("summary = %q", v.Summary)
	}
}
