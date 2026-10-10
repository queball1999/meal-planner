package plan

import (
	"context"
	"testing"

	"goeat/db"
)

func reviewKinds(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.Kind+" "+is.Day+" "+is.Slot)
	}
	return out
}

// One week holding each contradiction the check knows, and nothing flagged
// that is fine: a sound leftover, a day nobody is cooking, a day already gone.
func TestReviewPlanFindsContradictions(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	p := &db.Plan{WeekStart: "2026-01-04", WeekEnd: "2026-01-10"}
	meal := func(n int64, day, slot, title string, servings, cooked int) *db.Meal {
		return &db.Meal{ID: n, Day: day, Slot: slot, Title: title, Servings: servings, CookedPortions: cooked}
	}
	var meals []*db.Meal
	// Sun-Wed are full apart from what is under test; Thu-Sat are skipped.
	for _, day := range []string{"2026-01-04", "2026-01-05", "2026-01-06", "2026-01-07"} {
		meals = append(meals, meal(0, day, "breakfast", "Toast", 2, 2))
	}
	chili := meal(1, "2026-01-04", "dinner", "Chili", 2, 4)
	salmon := meal(2, "2026-01-05", "dinner", "Salmon", 2, 2)
	pie := meal(3, "2026-01-07", "dinner", "Pie", 2, 2)

	sound := meal(4, "2026-01-05", "lunch", "Leftover Chili", 2, 2)
	sound.IsLeftover, sound.LeftoverSourceMealID = true, id(1)
	short := meal(5, "2026-01-06", "lunch", "Leftover Salmon", 2, 2)
	short.IsLeftover, short.LeftoverSourceMealID = true, id(2)
	early := meal(6, "2026-01-07", "lunch", "Leftover Pie", 2, 2)
	early.IsLeftover, early.LeftoverSourceMealID = true, id(3)
	// Titled as leftovers, tied to nothing.
	orphan := meal(7, "2026-01-06", "dinner", "Leftover Stew", 2, 2)

	meals = append(meals, chili, salmon, pie, sound, short, early, orphan,
		meal(8, "2026-01-04", "lunch", "Soup", 2, 2))
	days := []*db.PlanDay{
		{Date: "2026-01-08", Status: db.DaySkipped},
		{Date: "2026-01-09", Status: db.DayEatingOut},
		{Date: "2026-01-10", Status: db.DaySkipped},
	}

	got := reviewKinds(ReviewPlan(p, meals, days, ""))
	want := []string{
		IssueCookShort + " 2026-01-05 dinner",
		IssueLeftoverNoSource + " 2026-01-06 dinner",
		IssueLeftoverTooEarly + " 2026-01-07 lunch",
	}
	if len(got) != len(want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("issue %d = %q, want %q", i, got[i], want[i])
		}
	}

	// From Wednesday on, only Wednesday's own problem is left to fix.
	got = reviewKinds(ReviewPlan(p, meals, days, "2026-01-07"))
	if len(got) != 1 || got[0] != IssueLeftoverTooEarly+" 2026-01-07 lunch" {
		t.Errorf("from wednesday: issues = %v", got)
	}
}

func TestReviewPlanFindsEmptySlots(t *testing.T) {
	p := &db.Plan{WeekStart: "2026-01-04", WeekEnd: "2026-01-10"}
	days := []*db.PlanDay{{Date: "2026-01-10", Status: db.DaySkipped}}

	issues := ReviewPlan(p, nil, days, "2026-01-09")
	got := reviewKinds(issues)
	// Friday's three slots; Saturday is skipped and the rest already happened.
	if len(got) != 3 || got[0] != IssueEmptySlot+" 2026-01-09 breakfast" || got[2] != IssueEmptySlot+" 2026-01-09 dinner" {
		t.Fatalf("issues = %v", got)
	}
	if s := issues[1].String(); s != "friday lunch has no meal" {
		t.Errorf("sentence = %q", s)
	}
}

// A meal short of its leftovers is made to cook the difference, and the plan
// then reviews clean.
func TestFixCookShortExtendsTheSource(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T", HouseholdSize: 2, WeeklyBudgetCents: 10000, Timezone: "UTC"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-04", WeekEnd: "2026-01-10", BudgetCents: 10000})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	chili, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-04", Slot: "dinner", Title: "Chili", Effort: "standard", Servings: 2, CookedPortions: 2})
	if err != nil {
		t.Fatalf("chili: %v", err)
	}
	left, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "lunch", Title: "Leftover Chili", Effort: "quick", Servings: 2, CookedPortions: 2})
	if err != nil {
		t.Fatalf("leftover: %v", err)
	}
	if err := store.UpdateMealLeftover(ctx, left.ID, true, &chili.ID); err != nil {
		t.Fatalf("link: %v", err)
	}

	// Only the leftover's source matters here, so every other slot is skipped.
	only := func() []Issue {
		meals, _ := store.ListMealsByPlan(ctx, p.ID)
		var out []Issue
		for _, is := range ReviewPlan(p, meals, nil, "") {
			if is.Kind != IssueEmptySlot {
				out = append(out, is)
			}
		}
		return out
	}

	issues := only()
	if len(issues) != 1 || issues[0].Kind != IssueCookShort || issues[0].Short != 2 {
		t.Fatalf("issues = %+v, want the chili 2 short", issues)
	}
	fixed, open, err := FixCookShort(ctx, store, issues)
	if err != nil || len(fixed) != 1 || len(open) != 0 {
		t.Fatalf("fix: fixed=%d open=%d err=%v", len(fixed), len(open), err)
	}
	if m, _ := store.GetMealByID(ctx, chili.ID); m.CookedPortions != 4 {
		t.Errorf("chili cooks %d, want 4", m.CookedPortions)
	}
	if again := only(); len(again) != 0 {
		t.Errorf("still wrong after the fix: %+v", again)
	}
}

// A lunch with nothing planned becomes the leftovers of the dinner the night
// before, that dinner cooks enough for it, and what leftovers cannot answer -
// a dinner, a lunch with no dinner before it - is handed back.
func TestFixWithLeftoversFillsNextDayLunch(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T", HouseholdSize: 2, WeeklyBudgetCents: 10000, Timezone: "UTC"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-04", WeekEnd: "2026-01-10", BudgetCents: 10000})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	add := func(day, slot, title string) *db.Meal {
		m, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: day, Slot: slot, Title: title, Effort: "standard", Servings: 2, CookedPortions: 2})
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		return m
	}
	// Sunday is whole. Monday has a dinner and no lunch; Tuesday has neither.
	add("2026-01-04", "lunch", "Soup")
	pie := add("2026-01-04", "dinner", "Shepherds Pie")
	tacos := add("2026-01-05", "dinner", "Tacos")

	issues := []Issue{
		{Kind: IssueEmptySlot, Day: "2026-01-05", Slot: "lunch"},
		{Kind: IssueEmptySlot, Day: "2026-01-06", Slot: "lunch"},
		{Kind: IssueEmptySlot, Day: "2026-01-06", Slot: "dinner"},
		{Kind: IssueEmptySlot, Day: "2026-01-07", Slot: "lunch"},
	}
	fixed, open, err := FixWithLeftovers(ctx, store, p.ID, issues, 2)
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if len(fixed) != 2 || fixed[0].Source.ID != pie.ID || fixed[1].Source.ID != tacos.ID {
		t.Fatalf("fixed = %+v, want Monday lunch from the pie and Tuesday lunch from the tacos", fixed)
	}
	// Tuesday dinner needs a dish; Wednesday lunch has no dinner before it.
	if got := reviewKinds(open); len(got) != 2 || got[0] != IssueEmptySlot+" 2026-01-06 dinner" || got[1] != IssueEmptySlot+" 2026-01-07 lunch" {
		t.Errorf("open = %v", got)
	}

	meals, _ := store.ListMealsByPlan(ctx, p.ID)
	var monLunch *db.Meal
	for _, m := range meals {
		if m.Day == "2026-01-05" && m.Slot == "lunch" {
			monLunch = m
		}
	}
	if monLunch == nil || monLunch.Title != "Leftover Shepherds Pie" || !monLunch.IsLeftover ||
		monLunch.LeftoverSourceMealID == nil || *monLunch.LeftoverSourceMealID != pie.ID {
		t.Errorf("monday lunch = %+v, want leftovers tied to the pie", monLunch)
	}
	if m, _ := store.GetMealByID(ctx, pie.ID); m.CookedPortions != 4 {
		t.Errorf("pie cooks %d, want 4", m.CookedPortions)
	}
	// What it wrote is sound: nothing left but the slots it handed back.
	for _, is := range ReviewPlan(p, meals, nil, "2026-01-05") {
		if is.Kind != IssueEmptySlot {
			t.Errorf("fix left a contradiction: %s", is)
		}
	}
}
