package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"goeat/db"
	"goeat/llm"
)

// fakeGenerator returns a fixed, schema-valid 21-meal plan every time it's
// called - enough to drive GenerateForWeek through persistence without a real LLM.
// A distinct titlePrefix per instance lets a test tell two generations apart.
type fakeGenerator struct {
	titlePrefix string
}

func (f *fakeGenerator) ProviderName() string { return "fake" }
func (f *fakeGenerator) ModelName() string    { return "fake-model" }

func (f *fakeGenerator) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	var meals []GeneratedMeal
	for _, day := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			meals = append(meals, GeneratedMeal{
				Day: day, Slot: slot,
				Title:  f.titlePrefix + " " + day + " " + slot,
				Effort: "quick", Servings: 2, CookedPortions: 2,
				Ingredients: []GeneratedIngredient{
					{Name: "canned black beans", Quantity: 1, Unit: "can", EstPriceCents: 129},
				},
				Steps: []string{"Cook it."},
			})
		}
	}
	raw, err := json.Marshal(GeneratedPlan{Meals: meals})
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	return llm.GenerateResponse{Content: string(raw)}, nil
}

// failingGenerator always errors, simulating an LLM call that fails outright.
type failingGenerator struct{}

func (failingGenerator) ProviderName() string { return "fake" }
func (failingGenerator) ModelName() string    { return "fake-model" }
func (failingGenerator) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, fmt.Errorf("simulated LLM failure")
}

func newGenerateTestStore(t *testing.T) (db.Store, int64) {
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
	return store, hh.ID
}

// TestGenerate_CancelsPreviousPlanForSameWeek reproduces the "regenerate"
// flow end to end: generating a second time for the same household (and
// therefore the same target week) must flag the first plan canceled - kept
// for /plan/history, but no longer the active plan for that week.
func TestGenerate_CancelsPreviousPlanForSameWeek(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	firstID, err := GenerateForWeek(ctx, store, &fakeGenerator{titlePrefix: "First"}, hhID, testWeek, testWeek, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	first, err := store.GetPlanByID(ctx, firstID)
	if err != nil || first == nil {
		t.Fatalf("get first plan: %v", err)
	}
	if first.Canceled {
		t.Fatal("first plan should not start canceled")
	}

	secondID, err := GenerateForWeek(ctx, store, &fakeGenerator{titlePrefix: "Second"}, hhID, testWeek, testWeek, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if secondID == firstID {
		t.Fatalf("regenerate should create a new plan row, got same id %d", firstID)
	}
	if first.WeekStart != "" {
		second, err := store.GetPlanByID(ctx, secondID)
		if err != nil || second == nil {
			t.Fatalf("get second plan: %v", err)
		}
		if second.WeekStart != first.WeekStart {
			t.Fatalf("expected both generations to target the same week, got %q vs %q", first.WeekStart, second.WeekStart)
		}
	}

	// The old plan must now read as canceled...
	first, err = store.GetPlanByID(ctx, firstID)
	if err != nil || first == nil {
		t.Fatalf("re-fetch first plan: %v", err)
	}
	if !first.Canceled {
		t.Fatal("first plan should be canceled after a successful regenerate for the same week")
	}
	if first.Status != "ready" {
		t.Fatalf("canceling must not change status, got %q", first.Status)
	}

	// ...while GetLatestPlan/GetPlanByWeekStart both resolve to the new one.
	latest, err := store.GetLatestPlan(ctx, hhID)
	if err != nil || latest == nil || latest.ID != secondID {
		t.Fatalf("latest plan = %+v, want id %d", latest, secondID)
	}
	byWeek, err := store.GetPlanByWeekStart(ctx, hhID, first.WeekStart)
	if err != nil || byWeek == nil || byWeek.ID != secondID {
		t.Fatalf("plan by week = %+v, want id %d", byWeek, secondID)
	}

	// ...and the canceled plan is still visible in history.
	all, err := store.ListPlans(ctx, hhID)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d plans in history, want 2 (canceled one kept)", len(all))
	}
}

// TestGenerate_CancelsOldPlanImmediatelyEvenOnFailure locks in "the moment we
// start generating a new plan, it clears the old plan for that week" - the
// cancel must happen right after the new plan row is created, not gated on
// the new generation succeeding. Regenerating and having the LLM call fail
// must still retire the old plan: GetLatestPlan/GetPlanByWeekStart already
// pick whichever row is newest regardless of status, so leaving the old plan
// uncanceled after a failure wouldn't actually keep it "active" anyway - it
// would just leave the cancellation unrecorded.
func TestGenerate_CancelsOldPlanImmediatelyEvenOnFailure(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	firstID, err := GenerateForWeek(ctx, store, &fakeGenerator{titlePrefix: "First"}, hhID, testWeek, testWeek, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}

	_, err = GenerateForWeek(ctx, store, failingGenerator{}, hhID, testWeek, testWeek, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected the second generate to fail")
	}

	first, err := store.GetPlanByID(ctx, firstID)
	if err != nil || first == nil {
		t.Fatalf("get first plan: %v", err)
	}
	if !first.Canceled {
		t.Fatal("first plan should be canceled the moment the regenerate started, even though it then failed")
	}
}
