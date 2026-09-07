package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goeat/db"
	"goeat/llm"
)

// newAgentTestStore builds a household with a one-week plan holding three
// named dinners, which is enough to exercise every plan verb.
func newAgentTestStore(t *testing.T) (*Session, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{
		Name: "T", HouseholdSize: 2, WeeklyBudgetCents: 10000, Timezone: "UTC",
	})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	// 2026-01-04 is a Sunday, so weekday names map cleanly onto the week.
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: hh.ID, WeekStart: "2026-01-04", WeekEnd: "2026-01-10", BudgetCents: 10000,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := store.UpdatePlanStatus(ctx, p.ID, "ready"); err != nil {
		t.Fatalf("status: %v", err)
	}

	seed := []struct{ day, slot, title string }{
		{"2026-01-04", "dinner", "Chicken Quesadillas"},
		{"2026-01-05", "dinner", "Weeknight Chili"},
		{"2026-01-06", "dinner", "Sheet Pan Salmon"},
	}
	for _, m := range seed {
		if _, err := store.CreateMeal(ctx, db.CreateMealParams{
			PlanID: p.ID, Day: m.day, Slot: m.slot, Title: m.title,
			Effort: "standard", Servings: 2, CookedPortions: 2,
		}); err != nil {
			t.Fatalf("meal %s: %v", m.title, err)
		}
	}

	return &Session{Store: store, Household: hh, HouseholdID: hh.ID}, p.ID
}

func mustCall(t *testing.T, r *Registry, s *Session, name string, args string) Result {
	t.Helper()
	res, err := r.Call(context.Background(), s, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	return res
}

func mealAt(t *testing.T, s *Session, planID int64, day, slot string) *db.Meal {
	t.Helper()
	meals, err := s.Store.ListMealsByPlan(context.Background(), planID)
	if err != nil {
		t.Fatalf("list meals: %v", err)
	}
	for _, m := range meals {
		if m.Day == day && m.Slot == slot {
			return m
		}
	}
	return nil
}

// The headline request from the brief: "move chicken quesadillas to monday".
func TestMoveMealByTitleAndWeekday(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	res := mustCall(t, r, s, "move_meal", `{"title":"chicken quesadillas","day":"monday"}`)
	if !strings.Contains(res.Summary, "monday") {
		t.Errorf("summary = %q, want it to name the destination", res.Summary)
	}

	// Monday of the plan week is 2026-01-05, which already held the chili -
	// so the chili must have been swapped back to Sunday, not deleted.
	moved := mealAt(t, s, planID, "2026-01-05", "dinner")
	if moved == nil || moved.Title != "Chicken Quesadillas" {
		t.Fatalf("Monday dinner = %+v, want Chicken Quesadillas", moved)
	}
	displaced := mealAt(t, s, planID, "2026-01-04", "dinner")
	if displaced == nil || displaced.Title != "Weeknight Chili" {
		t.Errorf("Sunday dinner = %+v, want the displaced Weeknight Chili", displaced)
	}
	if !strings.Contains(res.Summary, "Weeknight Chili") {
		t.Errorf("summary = %q, want it to mention what was displaced", res.Summary)
	}
}

// A destructive verb aimed at an ambiguous name must stop and ask, not guess.
func TestFindMealRefusesAmbiguousTitle(t *testing.T) {
	s, planID := newAgentTestStore(t)
	ctx := context.Background()
	if _, err := s.Store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-07", Slot: "dinner",
		Title: "Chicken Tacos", Effort: "quick", Servings: 2, CookedPortions: 2,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := NewRegistry()
	RegisterAll(r)
	_, err := r.Call(ctx, s, "delete_meal", json.RawMessage(`{"title":"chicken"}`))
	if err == nil {
		t.Fatal("an ambiguous title was accepted for a delete")
	}
	if !strings.Contains(err.Error(), "Chicken Quesadillas") || !strings.Contains(err.Error(), "Chicken Tacos") {
		t.Errorf("error = %q, want it to name both candidates", err)
	}
	// Nothing may have been deleted.
	meals, _ := s.Store.ListMealsByPlan(ctx, planID)
	if len(meals) != 4 {
		t.Errorf("%d meals remain, want 4 - an ambiguous delete removed something", len(meals))
	}
}

func TestResolveDayAcceptsWeekdayAndISO(t *testing.T) {
	p := &db.Plan{WeekStart: "2026-01-04", WeekEnd: "2026-01-10"}

	if got, err := resolveDay(p, "Tuesday"); err != nil || got != "2026-01-06" {
		t.Errorf("resolveDay(Tuesday) = %q, %v; want 2026-01-06", got, err)
	}
	if got, err := resolveDay(p, "2026-01-08"); err != nil || got != "2026-01-08" {
		t.Errorf("resolveDay(ISO) = %q, %v", got, err)
	}
	// A date outside the plan week is a mistake worth naming, not a silent
	// write into a week nobody is looking at.
	if _, err := resolveDay(p, "2026-02-01"); err == nil {
		t.Error("a date outside the plan week was accepted")
	}
	if _, err := resolveDay(p, "next thursday"); err == nil {
		t.Error("unparseable text was accepted as a day")
	}
}

// Required arguments are checked before a tool runs, so the model gets a
// precise message instead of a zero value reaching a query.
func TestRegistryValidatesRequiredArgs(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	_, err := r.Call(context.Background(), s, "set_day_status", json.RawMessage(`{"day":"monday"}`))
	if err == nil {
		t.Fatal("a missing required argument was accepted")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("error = %q, want it to name the missing argument", err)
	}
	// The failed attempt is still audited - "it tried and could not" is what a
	// user needs when the answer looks wrong.
	if len(s.Audit) != 1 || s.Audit[0].Error == "" {
		t.Errorf("audit = %+v, want one failed entry", s.Audit)
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	_, err := r.Call(context.Background(), s, "make_dinner_happen", json.RawMessage(`{}`))
	var unknown ErrUnknownTool
	if err == nil || !strings.Contains(err.Error(), "make_dinner_happen") {
		t.Fatalf("err = %v, want it to name the unknown tool", err)
	}
	if !asUnknown(err, &unknown) {
		t.Errorf("err type = %T, want ErrUnknownTool", err)
	}
}

func asUnknown(err error, target *ErrUnknownTool) bool {
	u, ok := err.(ErrUnknownTool)
	if ok {
		*target = u
	}
	return ok
}

// edit_meal must leave unspecified fields alone - a rename that silently reset
// the servings would quietly change the whole shopping list.
func TestEditMealKeepsUnspecifiedFields(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	mustCall(t, r, s, "edit_meal", `{"title":"Weeknight Chili","new_title":"Sunday Chili"}`)

	m := mealAt(t, s, planID, "2026-01-05", "dinner")
	if m == nil || m.Title != "Sunday Chili" {
		t.Fatalf("meal = %+v, want the new title", m)
	}
	if m.Servings != 2 {
		t.Errorf("servings = %d, want the original 2", m.Servings)
	}
	if m.Effort != "standard" {
		t.Errorf("effort = %q, want the original standard", m.Effort)
	}
}

// Marking a day must tell the user what it costs, rather than letting them
// discover an empty plate later.
func TestSetDayStatusReportsStrandedLeftovers(t *testing.T) {
	s, planID := newAgentTestStore(t)
	ctx := context.Background()

	source := mealAt(t, s, planID, "2026-01-05", "dinner")
	leftover, err := s.Store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: planID, Day: "2026-01-06", Slot: "lunch",
		Title: "Chili leftovers", Effort: "quick", Servings: 2, CookedPortions: 2,
	})
	if err != nil {
		t.Fatalf("seed leftover: %v", err)
	}
	if err := s.Store.UpdateMealLeftover(ctx, leftover.ID, true, &source.ID); err != nil {
		t.Fatalf("link leftover: %v", err)
	}

	r := NewRegistry()
	RegisterAll(r)
	res := mustCall(t, r, s, "set_day_status", `{"day":"monday","status":"eating_out"}`)

	if !strings.Contains(res.Summary, "Chili leftovers") {
		t.Errorf("summary = %q, want it to warn about the stranded meal", res.Summary)
	}
}

// set_pantry_qty sets, it does not accumulate - CreatePantryItem's upsert adds,
// which would inflate stock every time the assistant was asked.
func TestSetPantryQtyReplacesRatherThanAdds(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	mustCall(t, r, s, "set_pantry_qty", `{"name":"olive oil","quantity":2,"unit":"bottle"}`)
	mustCall(t, r, s, "set_pantry_qty", `{"name":"olive oil","quantity":3,"unit":"bottle"}`)

	rows, _ := s.Store.ListPantryItems(context.Background(), s.HouseholdID)
	var found bool
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Name), "olive oil") {
			found = true
			if row.QuantityOnHand != 3 {
				t.Errorf("quantity = %v, want 3 (set, not 2+3)", row.QuantityOnHand)
			}
		}
	}
	if !found {
		t.Error("olive oil was not added to the pantry")
	}
}

// Describe is what the model is actually steered by, so it has to name every
// tool and mark the dangerous ones.
func TestDescribeCoversEveryTool(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r)
	desc := r.Describe()

	for _, name := range r.Names() {
		if !strings.Contains(desc, name+"(") {
			t.Errorf("Describe omits %s", name)
		}
	}
	if !strings.Contains(desc, "[changes data]") {
		t.Error("Describe never marks a mutating tool")
	}
	// A read tool must not be marked as mutating.
	readPlanLine := lineContaining(desc, "read_plan(")
	if strings.Contains(readPlanLine, "[changes data]") {
		t.Errorf("read_plan is marked as mutating: %q", readPlanLine)
	}
}

func lineContaining(s, needle string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, needle) {
			return ln
		}
	}
	return ""
}

func TestRegistryRejectsDuplicateTool(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate tool did not panic")
		}
	}()
	r := NewRegistry()
	r.Register(&Tool{Name: "dup", Run: func(context.Context, *Session, json.RawMessage) (Result, error) {
		return Result{}, nil
	}})
	r.Register(&Tool{Name: "dup", Run: func(context.Context, *Session, json.RawMessage) (Result, error) {
		return Result{}, nil
	}})
}

// ── The loop ──────────────────────────────────────────────────────────────

// scriptedGen replays a fixed list of model replies, recording the prompts it
// was given so a test can assert what the model actually saw.
type scriptedGen struct {
	replies []string
	seen    []string
	i       int
}

func (g *scriptedGen) ProviderName() string { return "scripted" }
func (g *scriptedGen) ModelName() string    { return "scripted" }
func (g *scriptedGen) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	g.seen = append(g.seen, req.Prompt)
	if g.i >= len(g.replies) {
		return llm.GenerateResponse{Content: `{"say":"done"}`}, nil
	}
	out := g.replies[g.i]
	g.i++
	return llm.GenerateResponse{Content: out}, nil
}

func TestRunCallsToolThenAnswers(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"move_meal","args":{"title":"chicken quesadillas","day":"monday"}}`,
		`{"say":"Moved chicken quesadillas to Monday dinner."}`,
	}}
	runner := &Runner{Gen: gen, Registry: r}

	reply, err := runner.Run(context.Background(), s, nil, "move chicken quesadillas to monday")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply.Text != "Moved chicken quesadillas to Monday dinner." {
		t.Errorf("text = %q", reply.Text)
	}
	if len(reply.Audit) != 1 || reply.Audit[0].Tool != "move_meal" {
		t.Errorf("audit = %+v, want one move_meal entry", reply.Audit)
	}
	if mealAt(t, s, planID, "2026-01-05", "dinner").Title != "Chicken Quesadillas" {
		t.Error("the move did not actually happen")
	}
	// The tool result has to reach the model, or it is answering blind.
	if len(gen.seen) < 2 || !strings.Contains(gen.seen[1], "TOOL move_meal OK") {
		t.Errorf("second prompt did not carry the tool result: %q", gen.seen)
	}
}

// A tool error is the model's problem to fix, not a dead end for the user.
func TestRunFeedsToolErrorsBackToTheModel(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"move_meal","args":{"title":"nonexistent thing","day":"monday"}}`,
		`{"say":"I couldn't find a meal by that name."}`,
	}}
	runner := &Runner{Gen: gen, Registry: r}

	reply, err := runner.Run(context.Background(), s, nil, "move the nonexistent thing")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(gen.seen[1], "TOOL move_meal ERROR") {
		t.Errorf("the error was not fed back: %q", gen.seen[1])
	}
	if reply.Text == "" {
		t.Error("no answer after a recoverable tool error")
	}
	if len(reply.Audit) != 1 || reply.Audit[0].Error == "" {
		t.Errorf("audit = %+v, want the failure recorded", reply.Audit)
	}
}

// A model that never stops must not write forever.
func TestRunStopsAtStepLimit(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	loopForever := []string{}
	for i := 0; i < 20; i++ {
		loopForever = append(loopForever, `{"tool":"read_plan","args":{}}`)
	}
	gen := &scriptedGen{replies: loopForever}
	runner := &Runner{Gen: gen, Registry: r, MaxSteps: 3}

	reply, err := runner.Run(context.Background(), s, nil, "go round in circles")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reply.HitLimit {
		t.Error("HitLimit not set")
	}
	if reply.Steps != 3 {
		t.Errorf("steps = %d, want 3", reply.Steps)
	}
	// Whatever was done is still reported rather than silently discarded.
	if reply.Text == "" {
		t.Error("no text after hitting the limit")
	}
}

func TestParseAction(t *testing.T) {
	cases := []struct {
		name, in, wantTool, wantSay string
	}{
		{"plain tool", `{"tool":"read_plan","args":{}}`, "read_plan", ""},
		{"plain say", `{"say":"hello"}`, "", "hello"},
		{"fenced", "```json\n{\"say\":\"hi\"}\n```", "", "hi"},
		{"prose around it", `Sure! {"say":"hi"} Let me know.`, "", "hi"},
		// Models routinely omit "args" for a no-argument tool.
		{"missing args", `{"tool":"read_plan"}`, "read_plan", ""},
	}
	for _, c := range cases {
		got, err := parseAction(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Tool != c.wantTool || got.Say != c.wantSay {
			t.Errorf("%s: got tool=%q say=%q", c.name, got.Tool, got.Say)
		}
		if c.wantTool != "" && len(got.Args) == 0 {
			t.Errorf("%s: args should default to an empty object", c.name)
		}
	}

	for _, bad := range []string{"", "   ", "no json here", `{"nothing":"useful"}`, `{broken`} {
		if _, err := parseAction(bad); err == nil {
			t.Errorf("parseAction(%q) succeeded, want an error", bad)
		}
	}
}
