package web

import (
	"context"
	"testing"

	"goeat/db"
)

// A failed generation has no meals, so the dashboard card must not offer
// "View plan" for it: it falls back to the week's still-active good plan, or
// to nothing ("No plan yet").
func TestUsableDashPlanSkipsFailedGeneration(t *testing.T) {
	failed := &db.Plan{ID: 2, WeekStart: "2026-09-27", Status: "error"}
	good := &db.Plan{ID: 1, WeekStart: "2026-09-27", Status: "ready"}
	canceled := &db.Plan{ID: 3, WeekStart: "2026-09-27", Status: "ready", Canceled: true}
	otherWeek := &db.Plan{ID: 4, WeekStart: "2026-09-20", Status: "ready"}

	srv := &Server{store: &dashPlanStub{plans: []*db.Plan{failed, canceled, otherWeek, good}}}
	if got := srv.usableDashPlan(t.Context(), 1, failed); got != good {
		t.Errorf("got %+v, want the earlier ready plan for the same week", got)
	}

	srv = &Server{store: &dashPlanStub{plans: []*db.Plan{failed, canceled, otherWeek}}}
	if got := srv.usableDashPlan(t.Context(), 1, failed); got != nil {
		t.Errorf("got %+v, want nil - the week has no usable plan", got)
	}

	if got := srv.usableDashPlan(t.Context(), 1, good); got != good {
		t.Errorf("a ready plan must pass through unchanged, got %+v", got)
	}
}

// dashPlanStub embeds a nil db.Store like costStub: anything unexpected panics.
type dashPlanStub struct {
	db.Store
	plans []*db.Plan
}

func (d *dashPlanStub) ListPlans(context.Context, int64) ([]*db.Plan, error) {
	return d.plans, nil
}
