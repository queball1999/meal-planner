package web

import (
	"context"
	"testing"

	"goeat/db"
	"goeat/plan"
)

// TestBuildShoppingListView_StoppedPricingOutlivesTheRequest confirms
// plan.StopPricing's effect survives the request that triggered it: a page
// view after pricing was stopped must report "not still pricing" and treat
// every line as resolved, even if a slow provider call left a row genuinely
// pending in the database when the user navigates away and back before the
// background run has actually finished winding down.
func TestBuildShoppingListView_StoppedPricingOutlivesTheRequest(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T", WeeklyBudgetCents: 10000})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 10000})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, p.ID, "ready"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	// UpdatePlanStatus writes the DB row but doesn't mutate p itself (still
	// carrying CreatePlan's default status) - buildShoppingListView's early
	// "still generating" guard needs the refreshed row.
	p, err = store.GetPlanByID(ctx, p.ID)
	if err != nil || p == nil {
		t.Fatalf("get plan: %v", err)
	}
	if _, err := store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
		PlanID: p.ID, DisplayName: "canned black beans", BuyQuantity: 1, PackSize: 1,
		PurchaseUnit: "can", PriceSource: "estimate", Confidence: "estimate", Pending: true,
	}); err != nil {
		t.Fatalf("seed pending item: %v", err)
	}

	s := &Server{store: store}

	// plan.IsPricingStopped is keyed by plan ID in a process-wide map, and
	// every test in this package creates its own fresh :memory: database
	// whose first plan is also ID 1 - so a stopped flag can otherwise leak
	// either direction between this test and any other one reusing that ID.
	// Registering (and immediately clearing) a cancel func for this ID clears
	// any stale stopped flag exactly the way a genuine new pricing run would;
	// doing the same on cleanup keeps this test's own StopPricing call below
	// from leaking into whatever runs next.
	resetPricingStopped := func() {
		plan.RegisterPricingCancel(p.ID, func() {})
		plan.ClearPricingCancel(p.ID)
	}
	resetPricingStopped()
	t.Cleanup(resetPricingStopped)

	// Before stopping: a genuinely pending row reports "still pricing".
	view := s.buildShoppingListView(ctx, hh, p, false)
	if !view.Pricing {
		t.Fatal("expected Pricing=true before stop, with a pending row on the list")
	}
	if len(view.UnassignedItems) != 1 || view.UnassignedItems[0].Pending != true {
		t.Fatalf("expected one pending line before stop, got %+v", view.UnassignedItems)
	}

	// Simulate the user clicking "Stop pricing". No cancel func was ever
	// registered for this plan (as if the background run already finished,
	// or a slow provider call is still holding the row pending) - StopPricing
	// must still remember the plan was stopped either way.
	plan.StopPricing(p.ID)

	view = s.buildShoppingListView(ctx, hh, p, false)
	if view.Pricing {
		t.Fatal("expected Pricing=false once pricing was stopped, even with a row still pending in the database")
	}
	if len(view.UnassignedItems) != 1 || view.UnassignedItems[0].Pending {
		t.Fatalf("expected the line to render as resolved (not a loading skeleton) once stopped, got %+v", view.UnassignedItems)
	}
}
