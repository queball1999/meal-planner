package plan

import (
	"context"
	"testing"

	"goeat/db"
)

func mkMealTitled(t *testing.T, store db.Store, planID int64, day, slot, title string, servings, cooked int) int64 {
	t.Helper()
	m, err := store.CreateMeal(context.Background(), db.CreateMealParams{
		PlanID: planID, Day: day, Slot: slot, Title: title, Effort: "quick",
		Servings: servings, CookedPortions: cooked,
	})
	if err != nil {
		t.Fatalf("create meal %s %s: %v", day, slot, err)
	}
	return m.ID
}

func mkMeal(t *testing.T, store db.Store, planID int64, day, slot string, servings, cooked int) int64 {
	return mkMealTitled(t, store, planID, day, slot, slot+" "+day, servings, cooked)
}

func leftoverIDs(t *testing.T, store db.Store, planID int64) map[int64]bool {
	t.Helper()
	meals, err := store.ListMealsByPlan(context.Background(), planID)
	if err != nil {
		t.Fatalf("list meals: %v", err)
	}
	out := map[int64]bool{}
	for _, m := range meals {
		if m.IsLeftover {
			out[m.ID] = true
		}
	}
	return out
}

// Only a meal the model titled as leftovers is ever marked - fitting the
// portion count is not enough on its own. A household that batch-cooks
// dinner "just in case" must not have every next-day lunch silently
// relabeled as living off it.
func TestPlanLeftoversOnlyMarksTitledMeals(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Monday dinner cooks 6 for 2 -> 4 surplus portions available.
	monDinner := mkMealTitled(t, store, p.ID, "2026-01-05", "dinner", "Chili", 2, 6)
	// Tuesday lunch fits the portion math but was never titled as leftovers -
	// it is a real, freshly-planned meal and must stay one.
	tueLunch := mkMealTitled(t, store, p.ID, "2026-01-06", "lunch", "Turkey Sandwich", 2, 2)

	if err := PlanLeftovers(ctx, store, p.ID, true); err != nil {
		t.Fatalf("plan leftovers: %v", err)
	}

	got := leftoverIDs(t, store, p.ID)
	if got[monDinner] {
		t.Errorf("the source meal was marked a leftover")
	}
	if got[tueLunch] {
		t.Errorf("an untitled meal was marked a leftover purely on portion math")
	}
}

// A meal titled as leftovers is marked and tied to the nearest earlier meal
// with a matching surplus, but never reaches across the week or onto
// breakfast even when the title says so.
func TestPlanLeftoversRespectsSlotAndDayGap(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Monday dinner cooks 6 for 2 -> 4 surplus.
	monDinner := mkMealTitled(t, store, p.ID, "2026-01-05", "dinner", "Chili", 2, 6)
	// Tuesday breakfast: never a leftover target even with the title.
	tueBreakfast := mkMealTitled(t, store, p.ID, "2026-01-06", "breakfast", "Chili (leftovers)", 2, 2)
	// Tuesday lunch: the legitimate next-day target, titled as such.
	tueLunch := mkMealTitled(t, store, p.ID, "2026-01-06", "lunch", "Chili (leftovers)", 2, 2)
	// Friday lunch: titled as leftovers but four days later - too stale to
	// link to Monday's surplus.
	friLunch := mkMealTitled(t, store, p.ID, "2026-01-09", "lunch", "Chili (leftovers)", 2, 2)

	if err := PlanLeftovers(ctx, store, p.ID, true); err != nil {
		t.Fatalf("plan leftovers: %v", err)
	}

	got := leftoverIDs(t, store, p.ID)
	if got[monDinner] {
		t.Errorf("the source meal was marked a leftover")
	}
	if got[tueBreakfast] {
		t.Errorf("breakfast was marked a leftover target even though its title said so")
	}
	if !got[tueLunch] {
		t.Errorf("the next-day lunch was NOT marked a leftover")
	}

	meals, err := store.ListMealsByPlan(ctx, p.ID)
	if err != nil {
		t.Fatalf("list meals: %v", err)
	}
	for _, m := range meals {
		if m.ID == tueLunch {
			if m.LeftoverSourceMealID == nil || *m.LeftoverSourceMealID != monDinner {
				t.Errorf("tueLunch not tied back to its parent meal: got %v", m.LeftoverSourceMealID)
			}
		}
		if m.ID == friLunch && m.LeftoverSourceMealID != nil {
			t.Errorf("a lunch four days after the surplus was tied to it: %v", *m.LeftoverSourceMealID)
		}
	}
}

// Tolerance off: nothing is ever marked.
func TestPlanLeftoversNoopWhenToleranceOff(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	mkMealTitled(t, store, p.ID, "2026-01-05", "dinner", "Chili", 2, 6)
	mkMealTitled(t, store, p.ID, "2026-01-06", "lunch", "Chili (leftovers)", 2, 2)

	if err := PlanLeftovers(ctx, store, p.ID, false); err != nil {
		t.Fatalf("plan leftovers: %v", err)
	}
	if len(leftoverIDs(t, store, p.ID)) != 0 {
		t.Errorf("meals were marked leftover with tolerance off")
	}
}
