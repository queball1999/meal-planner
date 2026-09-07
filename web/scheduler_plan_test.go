package web

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"goeat/config"
	"goeat/db"
	"goeat/llm"
	"goeat/plan"
)

// fakeSchedGenerator returns a fixed, schema-valid 21-meal plan - enough to
// drive plan.Generate through persistence without a real LLM.
type fakeSchedGenerator struct{}

func (fakeSchedGenerator) ProviderName() string { return "fake" }
func (fakeSchedGenerator) ModelName() string    { return "fake-model" }

func (fakeSchedGenerator) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	type ingredient struct {
		Name          string  `json:"name"`
		Quantity      float64 `json:"quantity"`
		Unit          string  `json:"unit"`
		EstPriceCents int64   `json:"est_price_cents"`
	}
	type meal struct {
		Day            string       `json:"day"`
		Slot           string       `json:"slot"`
		Title          string       `json:"title"`
		Effort         string       `json:"effort"`
		Servings       int          `json:"servings"`
		CookedPortions int          `json:"cooked_portions"`
		Ingredients    []ingredient `json:"ingredients"`
		Steps          []string     `json:"steps"`
	}
	var meals []meal
	for _, day := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		for _, slot := range []string{"breakfast", "lunch", "dinner"} {
			meals = append(meals, meal{
				Day: day, Slot: slot, Title: "Meal " + day + " " + slot,
				Effort: "quick", Servings: 2, CookedPortions: 2,
				Ingredients: []ingredient{{Name: "canned black beans", Quantity: 1, Unit: "can", EstPriceCents: 129}},
				Steps:       []string{"Cook it."},
			})
		}
	}
	raw, err := json.Marshal(struct {
		Meals []meal `json:"meals"`
	}{meals})
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	return llm.GenerateResponse{Content: string(raw)}, nil
}

func newSchedulerTestServer(t *testing.T, autoPlanHour int) (*Server, *db.Household) {
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
	s := &Server{
		cfg:   &config.Config{AutoPlanHour: autoPlanHour},
		store: store,
		gen:   fakeSchedGenerator{},
		jobs:  plan.NewJobManager(),
	}
	return s, hh
}

// nextRealSunday mirrors plan.Generate's own nextSunday(time.Now()) so a test
// can compute "the Saturday before the week Generate will actually target"
// without needing to inject a clock into Generate itself (it deliberately
// always uses the real wall clock in production).
func nextRealSunday() time.Time {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, (7-int(today.Weekday()))%7)
}

func TestMaybeAutoGeneratePlan_Disabled(t *testing.T) {
	s, hh := newSchedulerTestServer(t, -1) // disabled
	saturday := nextRealSunday().AddDate(0, 0, -1)
	s.maybeAutoGeneratePlanAt(context.Background(), saturday.Add(20*time.Hour))
	if s.jobs.Get(hh.ID) != nil {
		t.Fatal("should not start a job when AutoPlanHour is -1")
	}
}

func TestMaybeAutoGeneratePlan_WrongDayOrHour(t *testing.T) {
	s, hh := newSchedulerTestServer(t, 20)
	saturday := nextRealSunday().AddDate(0, 0, -1)

	// Right hour, wrong day (Sunday, not Saturday).
	s.maybeAutoGeneratePlanAt(context.Background(), saturday.AddDate(0, 0, 1).Add(20*time.Hour))
	if s.jobs.Get(hh.ID) != nil {
		t.Fatal("should not start a job on a non-Saturday")
	}

	// Right day, wrong hour.
	s.maybeAutoGeneratePlanAt(context.Background(), saturday.Add(9*time.Hour))
	if s.jobs.Get(hh.ID) != nil {
		t.Fatal("should not start a job outside the configured hour")
	}
}

func TestMaybeAutoGeneratePlan_TriggersAndIsIdempotent(t *testing.T) {
	s, hh := newSchedulerTestServer(t, 20)
	ctx := context.Background()
	saturday := nextRealSunday().AddDate(0, 0, -1)
	firing := saturday.Add(20 * time.Hour)

	s.maybeAutoGeneratePlanAt(ctx, firing)
	job := s.jobs.Get(hh.ID)
	if job == nil {
		t.Fatal("expected a job to start at the configured Saturday hour")
	}
	<-job.Done()

	updated, err := s.store.GetPlanByID(ctx, job.PlanID)
	if err != nil || updated == nil {
		t.Fatalf("get generated plan: %v", err)
	}
	wantWeekStart := nextRealSunday().Format("2006-01-02")
	if updated.WeekStart != wantWeekStart {
		t.Fatalf("plan week_start = %q, want %q", updated.WeekStart, wantWeekStart)
	}

	// A second tick in the same hour must not start another generation - the
	// plan for that week already exists now.
	s.maybeAutoGeneratePlanAt(ctx, firing.Add(10*time.Minute))
	if s.jobs.Get(hh.ID) != nil {
		t.Fatal("second tick in the same hour should not start a new job")
	}
	all, _ := s.store.ListPlans(ctx, hh.ID)
	if len(all) != 1 {
		t.Fatalf("got %d plans, want exactly 1 (no duplicate auto-generation)", len(all))
	}
}
