package plan

import (
	"context"
	"reflect"
	"testing"
	"time"

	"goeat/db"
	"goeat/llm"
)

// testWeek is the week the generation tests plan: a Sunday-start one.
var testWeek = time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

// generateTestWeek is GenerateForWeek for testWeek with nothing asked for -
// what most generation tests want.
func generateTestWeek(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, pricer Pricer, checker PriceChecker, j *Job) (int64, error) {
	return GenerateForWeek(ctx, store, gen, householdID, testWeek, testWeek, pricer, checker, j, nil, nil, nil)
}

// A weekday name lands on that weekday whichever day the week starts on.
func TestDayInWeek(t *testing.T) {
	sunday := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	monday := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		weekStart time.Time
		day, want string
	}{
		{"sunday week, sunday", sunday, "sunday", "2026-10-11"},
		{"sunday week, monday", sunday, "Monday", "2026-10-12"},
		{"sunday week, saturday", sunday, "saturday", "2026-10-17"},
		{"monday week, monday is the first day", monday, "monday", "2026-10-12"},
		{"monday week, saturday", monday, "saturday", "2026-10-17"},
		{"monday week, sunday is the last day", monday, " sunday ", "2026-10-18"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DayInWeek(c.weekStart, c.day)
			if !ok || got.Format("2006-01-02") != c.want || got.Weekday().String() == "" {
				t.Errorf("DayInWeek(%s, %q) = %s ok=%v, want %s", c.weekStart.Format("2006-01-02"), c.day, got.Format("2006-01-02"), ok, c.want)
			}
		})
	}
	if _, ok := DayInWeek(sunday, "someday"); ok {
		t.Error("an unknown day name was accepted")
	}
}

// The remaining days of a Monday-start week run through Sunday, in order.
func TestDaysFrom_MondayWeek(t *testing.T) {
	monday := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if got, want := daysFrom(monday, monday), []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}; !reflect.DeepEqual(got, want) {
		t.Errorf("full monday week = %v, want %v", got, want)
	}
	if got, want := daysFrom(monday, monday.AddDate(0, 0, 5)), []string{"saturday", "sunday"}; !reflect.DeepEqual(got, want) {
		t.Errorf("monday week from saturday = %v, want %v", got, want)
	}
}

// A plan generated for a Monday-start week puts every meal on the weekday it
// was named for, inside Monday..Sunday - not shifted a day as "sunday = day 0"
// would.
func TestGenerateForWeek_MondayStart(t *testing.T) {
	store, hh := manualTestStore(t)
	ctx := context.Background()
	monday := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

	id, err := GenerateForWeek(ctx, store, &fakeGenerator{titlePrefix: "M"}, hh.ID, monday, monday, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("GenerateForWeek: %v", err)
	}
	p, _ := store.GetPlanByID(ctx, id)
	if p.WeekStart != "2026-10-12" || p.WeekEnd != "2026-10-18" {
		t.Fatalf("plan week = %s..%s, want 2026-10-12..2026-10-18", p.WeekStart, p.WeekEnd)
	}
	meals, err := store.ListMealsByPlan(ctx, id)
	if err != nil || len(meals) != 21 {
		t.Fatalf("got %d meals (err %v), want 21", len(meals), err)
	}
	perDay := map[string]int{}
	for _, m := range meals {
		if m.Day < p.WeekStart || m.Day > p.WeekEnd {
			t.Errorf("meal %q is on %s, outside the plan's week", m.Title, m.Day)
		}
		perDay[m.Day]++
	}
	if len(perDay) != 7 {
		t.Errorf("meals cover %d days, want all 7: %v", len(perDay), perDay)
	}
}
