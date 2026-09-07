package db_test

import (
	"context"
	"testing"

	"goeat/db"
)

func newPlansTestStore(t *testing.T) (db.Store, *db.Household) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(context.Background(), db.CreateHouseholdParams{Name: "T"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	return store, hh
}

// TestCancelOtherPlansForWeek reproduces the regenerate flow: a second plan is
// created for the same week, and once it's confirmed ready the first plan
// should be flagged canceled - kept for /plan/history, but no longer surfaced
// by GetLatestPlan or GetPlanByWeekStart.
func TestCancelOtherPlansForWeek(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	first, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, first.ID, "ready"); err != nil {
		t.Fatalf("mark first ready: %v", err)
	}

	second, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, second.ID, "ready"); err != nil {
		t.Fatalf("mark second ready: %v", err)
	}

	if err := store.CancelOtherPlansForWeek(ctx, hh.ID, "2026-01-05", second.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	latest, err := store.GetLatestPlan(ctx, hh.ID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil || latest.ID != second.ID {
		t.Fatalf("latest plan = %+v, want id %d", latest, second.ID)
	}

	byWeek, err := store.GetPlanByWeekStart(ctx, hh.ID, "2026-01-05")
	if err != nil {
		t.Fatalf("get by week: %v", err)
	}
	if byWeek == nil || byWeek.ID != second.ID {
		t.Fatalf("plan by week = %+v, want id %d", byWeek, second.ID)
	}

	// The canceled plan is still reachable by id, for archival viewing.
	archived, err := store.GetPlanByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if archived == nil || !archived.Canceled {
		t.Fatalf("archived plan = %+v, want canceled=true", archived)
	}

	// And it still shows up in the full history listing.
	all, err := store.ListPlans(ctx, hh.ID)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d plans in history, want 2 (canceled one kept)", len(all))
	}
}
