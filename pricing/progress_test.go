package pricing

import (
	"context"
	"strings"
	"testing"
	"time"

	"goeat/db"
)

// missFor is a provider that answers every term except the ones named, so a
// single list can exercise both the store path and the AI-estimate path.
type missFor struct{ miss map[string]bool }

func (m missFor) Name() string { return "fake" }
func (m missFor) Lookup(_ context.Context, term string, _ int64, _ string) (*PriceResult, error) {
	if m.miss[term] {
		return nil, nil
	}
	return &PriceResult{PriceCents: 250, PurchaseUnit: "each", PackSize: 1, Source: "cache", Confidence: ConfidenceCached, FetchedAt: time.Now()}, nil
}

// A run attached to ctx ends up describing every line: which ones a store
// priced, which fell through to the AI estimate, and a finished state the
// dialog can still show after the run is over.
func TestProgress_TracksAPricingRun(t *testing.T) {
	ctx := context.Background()
	store, hh := newCostingStore(t)
	gs, err := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: "Corner Market", Kind: "grocery"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	p, _ := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-01-05", WeekEnd: "2026-01-11", BudgetCents: 100000})
	meal, _ := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-01-05", Slot: "dinner", Title: "X", Effort: "quick", Servings: 2, CookedPortions: 2})
	for _, n := range []string{"flour", "sugar", "butter"} {
		if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{MealID: meal.ID, Name: n, Quantity: 1, Unit: "each"}); err != nil {
			t.Fatalf("ingredient %s: %v", n, err)
		}
	}

	prog := StartProgress(p.ID, "Reprice")
	if GetProgress(p.ID) != prog {
		t.Fatal("GetProgress did not return the run StartProgress recorded")
	}
	if s := prog.Snapshot(); !s.Active || s.Phase != PhasePreparing || s.Total != 0 {
		t.Fatalf("fresh run = %+v, want active, preparing, no lines yet", s)
	}

	chain := NewChain([]PriceProvider{missFor{miss: map[string]bool{"butter": true}}, NewAIEstimateProvider(&fakeBatchGen{}, "")})
	runCtx := WithProgress(ctx, prog)
	if _, err := CostPlan(runCtx, store, chain, p.ID, hh, []*db.GroceryStore{gs}); err != nil {
		t.Fatalf("cost plan: %v", err)
	}
	prog.Finish(runCtx.Err())

	s := prog.Snapshot()
	if s.Active || s.Phase != PhaseDone || s.Status != "Finished" {
		t.Fatalf("finished run: active=%v phase=%q status=%q", s.Active, s.Phase, s.Status)
	}
	if s.Total != 3 || s.Done != 3 || s.Pass != 1 {
		t.Fatalf("total/done/pass = %d/%d/%d, want 3/3/1", s.Total, s.Done, s.Pass)
	}
	if s.Counts.Store != 2 || s.Counts.AI != 1 || s.Counts.Pending != 0 || s.Counts.Unpriced != 0 {
		t.Fatalf("counts = %+v, want 2 store, 1 AI", s.Counts)
	}
	for _, ln := range s.Lines {
		if ln.Status != LinePriced || ln.Store != "Corner Market" || ln.PriceCents <= 0 {
			t.Errorf("line %+v, want priced at Corner Market", ln)
		}
		wantSource := "Cached price"
		if ln.Name == "butter" {
			wantSource = "AI estimate"
		}
		if ln.Source != wantSource {
			t.Errorf("%s source = %q, want %q", ln.Name, ln.Source, wantSource)
		}
	}

	var log strings.Builder
	for _, ev := range s.Events {
		log.WriteString(ev.Message + "\n")
	}
	for _, want := range []string{"Started: Reprice", "Looking up 3 item(s) across 1 store(s)", "butter: no store price, queued for an AI estimate", "AI estimate: batch 1 of 1 (1 items)", "Finished in"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("activity log is missing %q:\n%s", want, log.String())
		}
	}
}

// Pricing code reports to ProgressFrom(ctx) unconditionally, so a nil run
// (no dialog watching: tests, the mid-draft price check) must be a no-op.
func TestProgress_NilIsANoOp(t *testing.T) {
	var p *Progress
	p.Note("x")
	p.Warn("x")
	p.SetPhase(PhaseAI)
	p.beginPass([]string{"a"}, 1)
	p.lineLooking(0)
	p.lineQueued(0)
	p.linePriced(0, HowStore, "cache", "S", 1)
	p.lineFinal(0, "", "", "", 0)
	p.MarkStopped()
	p.Finish(nil)
	if got := ProgressFrom(WithProgress(context.Background(), nil)); got != nil {
		t.Fatalf("ProgressFrom = %v, want nil", got)
	}
}

func TestProgress_StopAndUnpricedLines(t *testing.T) {
	p := StartProgress(-1, "New plan")
	p.beginPass([]string{"saffron", "salt"}, 2)
	p.lineLooking(0)
	if s := p.Snapshot(); s.Status != "Checking store prices: saffron" {
		t.Fatalf("status = %q", s.Status)
	}

	p.MarkStopped()
	p.MarkStopped() // a second click must not log twice
	s := p.Snapshot()
	if !s.Stopped || !s.Active || !strings.HasPrefix(s.Status, "Stopping") {
		t.Fatalf("after stop: %+v", s)
	}

	p.lineFinal(0, "", "", "", 0)
	p.lineFinal(1, HowGuess, "estimate", "", 120)
	p.Finish(context.Canceled)
	s = p.Snapshot()
	if s.Active || s.Status != "Stopped" {
		t.Fatalf("finished stopped run: active=%v status=%q", s.Active, s.Status)
	}
	if s.Counts.Unpriced != 1 || s.Counts.Guess != 1 || s.Done != 2 {
		t.Fatalf("counts = %+v done = %d", s.Counts, s.Done)
	}
	stops := 0
	for _, ev := range s.Events {
		if strings.HasPrefix(ev.Message, "Stop requested") {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("logged %d stop events, want 1", stops)
	}
}

func TestProgress_EventLogIsBounded(t *testing.T) {
	p := StartProgress(-2, "Reprice")
	for i := 0; i < maxProgressEvents*2; i++ {
		p.Note("tick")
	}
	p.Note("last")
	ev := p.Snapshot().Events
	if len(ev) != maxProgressEvents || ev[len(ev)-1].Message != "last" {
		t.Fatalf("kept %d events ending %q, want %d ending \"last\"", len(ev), ev[len(ev)-1].Message, maxProgressEvents)
	}
}
