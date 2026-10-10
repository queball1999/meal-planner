package plan

import (
	"context"
	"testing"
	"time"

	"goeat/db"
)

// Planning with no week named means the week containing today - except on
// that week's last day, when it means the week after.
func TestPlanningWeek(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := []struct {
		name, now, startDay, want string
		rolled                    bool
	}{
		{"sunday week, mid-week", "2026-10-07", "sunday", "2026-10-04", false},
		{"sunday week, first day", "2026-10-04", "sunday", "2026-10-04", false},
		{"sunday week, saturday rolls on", "2026-10-10", "sunday", "2026-10-11", true},
		{"monday week, saturday stays", "2026-10-10", "monday", "2026-10-05", false},
		{"monday week, sunday rolls on", "2026-10-11", "monday", "2026-10-12", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rolled := PlanningWeek(day(c.now), c.startDay)
			if got.Format("2006-01-02") != c.want || rolled != c.rolled {
				t.Errorf("PlanningWeek(%s, %s) = %s rolled=%v, want %s rolled=%v",
					c.now, c.startDay, got.Format("2006-01-02"), rolled, c.want, c.rolled)
			}
		})
	}
}

func manualTestStore(t *testing.T) (db.Store, *db.Household) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(context.Background(), db.CreateHouseholdParams{
		Name: "T", HouseholdSize: 2, WeeklyBudgetCents: 10000, Timezone: "UTC",
	})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	return store, hh
}

// A hand-built plan is ready and empty with every day seeded, and asking for
// the same week again hands back that plan instead of replacing it.
func TestCreateManual(t *testing.T) {
	store, hh := manualTestStore(t)
	ctx := context.Background()
	week := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	p, created, err := CreateManual(ctx, store, hh.ID, week)
	if err != nil || !created {
		t.Fatalf("CreateManual = created %v, err %v", created, err)
	}
	if p.Status != "ready" || p.WeekStart != "2026-10-11" || p.WeekEnd != "2026-10-17" {
		t.Errorf("plan = %+v, want a ready plan for Oct 11-17", p)
	}
	if days, _ := store.ListPlanDays(ctx, p.ID); len(days) != 7 {
		t.Errorf("seeded %d plan days, want 7", len(days))
	}

	again, created, err := CreateManual(ctx, store, hh.ID, week)
	if err != nil || created || again.ID != p.ID {
		t.Errorf("second CreateManual = plan %d created %v err %v, want plan %d untouched", again.ID, created, err, p.ID)
	}
}

func manualTestMeal(t *testing.T, store db.Store, planID int64, day, slot, title string, servings, cooked int) *db.Meal {
	t.Helper()
	ctx := context.Background()
	m, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: day, Slot: slot, Title: title, Effort: "standard",
		Servings: servings, CookedPortions: cooked,
	})
	if err != nil {
		t.Fatalf("create meal %s: %v", title, err)
	}
	if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
		MealID: m.ID, Name: "rice", Quantity: 200, Unit: "g", NormalizedTerm: "rice",
	}); err != nil {
		t.Fatalf("ingredient for %s: %v", title, err)
	}
	return m
}

// Eating a meal's leftovers makes that meal cook more - but only by what it
// is short, and never by less than the ingredients it then needs.
func TestAddLeftoverMeal(t *testing.T) {
	store, hh := manualTestStore(t)
	ctx := context.Background()
	p, _, err := CreateManual(ctx, store, hh.ID, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	chili := manualTestMeal(t, store, p.ID, "2026-10-12", "dinner", "Chili", 2, 2)

	res, err := AddLeftoverMeal(ctx, store, LeftoverParams{
		PlanID: p.ID, SourceMealID: chili.ID, Date: "2026-10-13", Slot: "lunch", Portions: 2,
	})
	if err != nil {
		t.Fatalf("AddLeftoverMeal: %v", err)
	}
	if res.ExtraCooked != 2 || res.Title != "Leftover Chili" {
		t.Errorf("result = %+v, want Leftover Chili with 2 extra cooked", res)
	}

	src, _ := store.GetMealByID(ctx, chili.ID)
	if src.Servings != 2 || src.CookedPortions != 4 || src.BaseCookedPortions != 4 {
		t.Errorf("source = %d srv, %d cooked (base %d), want 2 srv, 4 cooked (base 4)",
			src.Servings, src.CookedPortions, src.BaseCookedPortions)
	}
	ings, _ := store.ListIngredientsByMeal(ctx, chili.ID)
	if len(ings) != 1 || ings[0].Quantity != 400 || ings[0].BaseQuantity != 400 {
		t.Errorf("source ingredients = %+v, want rice doubled to 400", ings)
	}
	left, _ := store.GetMealByID(ctx, res.MealID)
	if !left.IsLeftover || left.LeftoverSourceMealID == nil || *left.LeftoverSourceMealID != chili.ID {
		t.Errorf("leftover meal = %+v, want it tied to the chili", left)
	}

	// A rescale of the source's day keeps the extra: 2 people still means
	// cooking for 4.
	if _, err := store.ScaleMealsForDay(ctx, p.ID, "2026-10-12", 2); err != nil {
		t.Fatalf("rescale: %v", err)
	}
	if src, _ = store.GetMealByID(ctx, chili.ID); src.CookedPortions != 4 {
		t.Errorf("after rescale the source cooks %d, want it to still cook 4", src.CookedPortions)
	}

	// Leftovers cannot come from a later meal.
	if _, err := AddLeftoverMeal(ctx, store, LeftoverParams{
		PlanID: p.ID, SourceMealID: chili.ID, Date: "2026-10-12", Slot: "lunch", Portions: 2,
	}); err == nil {
		t.Error("leftovers served before their source meal were accepted")
	}
}

// A source that already cooks a surplus nobody eats feeds the new slot from
// that before growing.
func TestAddLeftoverMeal_UsesSpareFirst(t *testing.T) {
	store, hh := manualTestStore(t)
	ctx := context.Background()
	p, _, err := CreateManual(ctx, store, hh.ID, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	stew := manualTestMeal(t, store, p.ID, "2026-10-12", "dinner", "Stew", 2, 5)

	res, err := AddLeftoverMeal(ctx, store, LeftoverParams{
		PlanID: p.ID, SourceMealID: stew.ID, Date: "2026-10-13", Slot: "dinner", Portions: 2,
	})
	if err != nil {
		t.Fatalf("first leftover: %v", err)
	}
	if res.ExtraCooked != 0 {
		t.Errorf("first leftover grew the source by %d, want 0 (3 spare)", res.ExtraCooked)
	}
	res, err = AddLeftoverMeal(ctx, store, LeftoverParams{
		PlanID: p.ID, SourceMealID: stew.ID, Date: "2026-10-14", Slot: "lunch", Portions: 2,
	})
	if err != nil {
		t.Fatalf("second leftover: %v", err)
	}
	if res.ExtraCooked != 1 {
		t.Errorf("second leftover grew the source by %d, want 1 (1 spare left)", res.ExtraCooked)
	}
}

func TestLeftoverSources(t *testing.T) {
	src := int64(1)
	meals := []*db.Meal{
		{ID: 1, Day: "2026-10-11", Slot: "dinner", Title: "Old roast"},
		{ID: 2, Day: "2026-10-13", Slot: "dinner", Title: "Chili"},
		{ID: 3, Day: "2026-10-14", Slot: "lunch", Title: "Leftover Chili", IsLeftover: true, LeftoverSourceMealID: &src},
		{ID: 4, Day: "2026-10-14", Slot: "dinner", Title: "Skipped", Status: db.DaySkipped},
		{ID: 5, Day: "2026-10-15", Slot: "lunch", Title: "Soup"},
		{ID: 6, Day: "2026-10-15", Slot: "dinner", Title: "Later"},
	}
	got := LeftoverSources(meals, "2026-10-15", "dinner")
	var ids []int64
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	// Nearest first; the roast is 4 days back, and leftovers, skipped meals
	// and the slot itself are out.
	if len(ids) != 2 || ids[0] != 5 || ids[1] != 2 {
		t.Errorf("LeftoverSources = %v, want [5 2]", ids)
	}
}

func TestPlanMove(t *testing.T) {
	chili, roast := int64(1), int64(4)
	meals := []*db.Meal{
		{ID: 1, Day: "2026-10-12", Slot: "dinner", Title: "Chili"},
		{ID: 2, Day: "2026-10-13", Slot: "lunch", Title: "Leftover Chili", IsLeftover: true, LeftoverSourceMealID: &chili},
		{ID: 3, Day: "2026-10-13", Slot: "dinner", Title: "Pasta"},
		{ID: 4, Day: "2026-10-14", Slot: "dinner", Title: "Roast"},
		{ID: 5, Day: "2026-10-15", Slot: "lunch", Title: "Leftover Roast", IsLeftover: true, LeftoverSourceMealID: &roast},
	}
	strandedIDs := func(im MoveImpact) []int64 {
		var ids []int64
		for _, m := range im.Stranded {
			ids = append(ids, m.ID)
		}
		return ids
	}

	// An empty slot, nothing depending on the move.
	im, err := PlanMove(meals, 3, "2026-10-16", "dinner", "")
	if err != nil || im.Displaced != nil || len(im.Stranded) != 0 {
		t.Errorf("move to an empty slot: %+v err %v, want nothing disturbed", im, err)
	}

	// A source moved past the meal eating its leftovers strands that meal.
	im, _ = PlanMove(meals, 1, "2026-10-16", "lunch", "")
	if ids := strandedIDs(im); len(ids) != 1 || ids[0] != 2 {
		t.Errorf("chili moved after its leftovers: stranded %v, want [2]", ids)
	}

	// A leftover dragged before its own source is itself stranded.
	im, _ = PlanMove(meals, 2, "2026-10-12", "lunch", "")
	if ids := strandedIDs(im); len(ids) != 1 || ids[0] != 2 {
		t.Errorf("leftovers moved before the chili: stranded %v, want [2]", ids)
	}

	// Swapping onto an occupied slot: the roast drops back to Tuesday, still
	// ahead of its leftovers, so nothing is stranded...
	im, _ = PlanMove(meals, 3, "2026-10-14", "dinner", MoveSwap)
	if im.Displaced == nil || im.Displaced.ID != 4 || len(im.Stranded) != 0 {
		t.Errorf("swap pasta with roast: %+v, want roast displaced and nothing stranded", im)
	}
	// ...but replacing it removes the roast its leftovers needed.
	im, _ = PlanMove(meals, 3, "2026-10-14", "dinner", MoveReplace)
	if ids := strandedIDs(im); len(ids) != 1 || ids[0] != 5 {
		t.Errorf("replace roast with pasta: stranded %v, want [5]", ids)
	}

	if _, err := PlanMove(meals, 99, "2026-10-14", "dinner", ""); err == nil {
		t.Error("moving a meal that is not on the plan was accepted")
	}
}

// The real move: swap by default, replace on request, and stranded leftovers
// settled the way the caller chose.
func TestMoveMeal(t *testing.T) {
	store, hh := manualTestStore(t)
	ctx := context.Background()
	p, _, err := CreateManual(ctx, store, hh.ID, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	chili := manualTestMeal(t, store, p.ID, "2026-10-12", "dinner", "Chili", 2, 2)
	pasta := manualTestMeal(t, store, p.ID, "2026-10-13", "dinner", "Pasta", 2, 2)
	left, err := AddLeftoverMeal(ctx, store, LeftoverParams{
		PlanID: p.ID, SourceMealID: chili.ID, Date: "2026-10-13", Slot: "lunch", Portions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Swap: pasta and chili trade places, which puts the chili after the
	// lunch that eats it. Unlinked by default - still a leftover, no source.
	res, err := MoveMeal(ctx, store, MoveParams{MealID: pasta.ID, Date: "2026-10-12", Slot: "dinner"})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if res.Displaced != "Chili" || res.Replaced || res.Stranded != 1 {
		t.Errorf("swap result = %+v, want Chili displaced, 1 stranded", res)
	}
	if m, _ := store.GetMealByID(ctx, chili.ID); m.Day != "2026-10-13" || m.Slot != "dinner" {
		t.Errorf("chili is at %s %s, want 2026-10-13 dinner", m.Day, m.Slot)
	}
	if m, _ := store.GetMealByID(ctx, left.MealID); !m.IsLeftover || m.LeftoverSourceMealID != nil {
		t.Errorf("stranded leftover = %+v, want still a leftover with no source", m)
	}

	// Replace: pasta lands on the chili's slot and the chili is gone.
	res, err = MoveMeal(ctx, store, MoveParams{
		MealID: pasta.ID, Date: "2026-10-13", Slot: "dinner", Occupied: MoveReplace, Leftovers: MoveClear,
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !res.Replaced || res.Displaced != "Chili" {
		t.Errorf("replace result = %+v, want Chili replaced", res)
	}
	if m, _ := store.GetMealByID(ctx, chili.ID); m != nil {
		t.Errorf("chili survived being replaced: %+v", m)
	}

	// A locked meal is not replaced.
	soup := manualTestMeal(t, store, p.ID, "2026-10-15", "dinner", "Soup", 2, 2)
	if err := store.UpdateMealLocked(ctx, soup.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := MoveMeal(ctx, store, MoveParams{
		MealID: pasta.ID, Date: "2026-10-15", Slot: "dinner", Occupied: MoveReplace,
	}); err != ErrMealLocked {
		t.Errorf("replacing a locked meal: err = %v, want ErrMealLocked", err)
	}
}

// A plan remembers the request it was generated from.
func TestPlanRequestRoundTrip(t *testing.T) {
	store, hh := manualTestStore(t)
	ctx := context.Background()
	p, _, err := CreateManual(ctx, store, hh.ID, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetPlanRequest(ctx, p.ID); err != nil || got != nil {
		t.Fatalf("a plan built by hand has request %+v err %v, want none", got, err)
	}

	want := db.PlanRequest{
		Recipes: []db.PlanRequestRecipe{{ID: 7, Title: "Lasagna"}},
		Text:    "salmon on Friday",
		OnHand:  []db.PlanRequestOnHand{{Name: "Rice", Quantity: 2, Unit: "kg"}},
		Scope:   "full",
	}
	if err := store.SetPlanRequest(ctx, p.ID, want); err != nil {
		t.Fatalf("SetPlanRequest: %v", err)
	}
	got, err := store.GetPlanRequest(ctx, p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPlanRequest: %+v err %v", got, err)
	}
	if got.Text != want.Text || len(got.Recipes) != 1 || got.Recipes[0] != want.Recipes[0] ||
		len(got.OnHand) != 1 || got.OnHand[0] != want.OnHand[0] {
		t.Errorf("request came back as %+v, want %+v", got, want)
	}
	list, err := store.ListPlanRequests(ctx, hh.ID, 10)
	if err != nil || len(list) != 1 || list[0].PlanID != p.ID || list[0].WeekStart != "2026-10-11" {
		t.Errorf("ListPlanRequests = %+v err %v, want just this plan", list, err)
	}
}
