package web

import (
	"context"
	"testing"

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
