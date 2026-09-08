package plan

import (
	"context"
	"testing"

	"goeat/db"
)

func mkMeal(t *testing.T, store db.Store, planID int64, day, slot string, servings, cooked int) int64 {
	t.Helper()
	m, err := store.CreateMeal(context.Background(), db.CreateMealParams{
		PlanID: planID, Day: day, Slot: slot, Title: slot + " " + day, Effort: "quick",
		Servings: servings, CookedPortions: cooked,
	})
	if err != nil {
		t.Fatalf("create meal %s %s: %v", day, slot, err)
	}
	return m.ID
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

// A batch-cooked dinner covers the next day's lunch, but its surplus must not
// reach across the week to tag an unrelated breakfast or a meal three days out.
func TestPlanLeftoversRespectsSlotAndDayGap(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Monday dinner cooks 6 for 2 -> 4 surplus.
	monDinner := mkMeal(t, store, p.ID, "2026-01-05", "dinner", 2, 6)
	// Tuesday breakfast: never a leftover target even though the count fits.
	tueBreakfast := mkMeal(t, store, p.ID, "2026-01-06", "breakfast", 2, 2)
	// Tuesday lunch: the legitimate next-day target.
	tueLunch := mkMeal(t, store, p.ID, "2026-01-06", "lunch", 2, 2)
	// Friday lunch: within portion budget but four days later - must stay a
	// real meal.
	friLunch := mkMeal(t, store, p.ID, "2026-01-09", "lunch", 2, 2)

	if err := PlanLeftovers(ctx, store, p.ID, true); err != nil {
		t.Fatalf("plan leftovers: %v", err)
	}

	got := leftoverIDs(t, store, p.ID)
	if got[monDinner] {
		t.Errorf("the source meal was marked a leftover")
	}
	if got[tueBreakfast] {
		t.Errorf("breakfast was marked a leftover target")
	}
	if got[friLunch] {
		t.Errorf("a lunch four days after the surplus was marked a leftover")
	}
	if !got[tueLunch] {
		t.Errorf("the next-day lunch was NOT marked a leftover")
	}
}

// Tolerance off: nothing is ever marked.
func TestPlanLeftoversNoopWhenToleranceOff(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)
	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hhID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	mkMeal(t, store, p.ID, "2026-01-05", "dinner", 2, 6)
	mkMeal(t, store, p.ID, "2026-01-06", "lunch", 2, 2)

	if err := PlanLeftovers(ctx, store, p.ID, false); err != nil {
		t.Fatalf("plan leftovers: %v", err)
	}
	if len(leftoverIDs(t, store, p.ID)) != 0 {
		t.Errorf("meals were marked leftover with tolerance off")
	}
}
