package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A mutating call must not reach the database before someone says yes.
func TestRunHoldsMutatingCall(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"move_meal","args":{"title":"chicken quesadillas","day":"monday"}}`,
		`{"say":"Done."}`,
	}}
	runner := &Runner{Gen: gen, Registry: r, ConfirmMutations: true}

	reply, err := runner.Run(context.Background(), s, nil, "move chicken quesadillas to monday")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply.Pending == nil {
		t.Fatal("a mutating call was not held")
	}
	if reply.Text != "" {
		t.Errorf("Text = %q, want empty - the assistant is waiting, not finished", reply.Text)
	}
	if reply.Pending.Tool != "move_meal" {
		t.Errorf("held %q", reply.Pending.Tool)
	}
	// The description and the arguments both matter: one says what the tool
	// does, the other says what it will do to.
	if !strings.Contains(reply.Pending.Description, "Move a meal") {
		t.Errorf("Description = %q", reply.Pending.Description)
	}
	if !strings.Contains(reply.Pending.Detail, "monday") {
		t.Errorf("Detail = %q, want it to name the destination", reply.Pending.Detail)
	}

	// Nothing moved.
	if m := mealAt(t, s, planID, "2026-01-04", "dinner"); m == nil || m.Title != "Chicken Quesadillas" {
		t.Errorf("Sunday dinner = %+v - the held call ran anyway", m)
	}
	if len(s.Audit) != 0 {
		t.Errorf("audit = %+v, want empty - nothing was executed", s.Audit)
	}
}

// A read tool is not held: "what's on the list?" has to stay a single
// frictionless turn.
func TestRunDoesNotHoldReadTools(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"read_plan","args":{}}`,
		`{"say":"Three dinners this week."}`,
	}}
	runner := &Runner{Gen: gen, Registry: r, ConfirmMutations: true}

	reply, err := runner.Run(context.Background(), s, nil, "what's planned?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply.Pending != nil {
		t.Fatalf("a read tool was held: %+v", reply.Pending)
	}
	if reply.Text == "" {
		t.Error("no answer from a read-only turn")
	}
}

func TestResumeApprovedRunsTheCall(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"move_meal","args":{"title":"chicken quesadillas","day":"monday"}}`,
		`{"say":"Moved it to Monday."}`,
	}}
	runner := &Runner{Gen: gen, Registry: r, ConfirmMutations: true}
	ctx := context.Background()

	held, err := runner.Run(ctx, s, nil, "move chicken quesadillas to monday")
	if err != nil || held.Pending == nil {
		t.Fatalf("expected a held call: %v / %+v", err, held.Pending)
	}

	reply, err := runner.Resume(ctx, s, held.Pending, true)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if reply.Text != "Moved it to Monday." {
		t.Errorf("Text = %q", reply.Text)
	}
	if m := mealAt(t, s, planID, "2026-01-05", "dinner"); m == nil || m.Title != "Chicken Quesadillas" {
		t.Errorf("Monday dinner = %+v, want the moved meal", m)
	}
	if len(reply.Audit) != 1 || reply.Audit[0].Tool != "move_meal" {
		t.Errorf("audit = %+v, want the approved call recorded", reply.Audit)
	}
	// The model has to see the result, or it answers blind.
	if !strings.Contains(gen.seen[1], "TOOL move_meal OK") {
		t.Errorf("the resumed prompt did not carry the result: %q", gen.seen[1])
	}
}

// Declining is a conversation, not a dead end: the model is told and can ask
// what was actually wanted.
func TestResumeDeclinedDoesNotRun(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"move_meal","args":{"title":"chicken quesadillas","day":"monday"}}`,
		`{"say":"No problem - which day did you mean?"}`,
	}}
	runner := &Runner{Gen: gen, Registry: r, ConfirmMutations: true}
	ctx := context.Background()

	held, _ := runner.Run(ctx, s, nil, "move chicken quesadillas to monday")
	if held.Pending == nil {
		t.Fatal("nothing was held")
	}

	reply, err := runner.Resume(ctx, s, held.Pending, false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if m := mealAt(t, s, planID, "2026-01-04", "dinner"); m == nil || m.Title != "Chicken Quesadillas" {
		t.Errorf("a declined call ran anyway: Sunday dinner = %+v", m)
	}
	if len(s.Audit) != 0 {
		t.Errorf("audit = %+v, want empty", s.Audit)
	}
	if !strings.Contains(gen.seen[1], "declined") {
		t.Errorf("the model was not told about the refusal: %q", gen.seen[1])
	}
	if reply.Text == "" {
		t.Error("declining ended the conversation instead of continuing it")
	}
}

func TestResumeWithoutPendingErrors(t *testing.T) {
	s, _ := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)
	runner := &Runner{Gen: &scriptedGen{}, Registry: r, ConfirmMutations: true}

	if _, err := runner.Resume(context.Background(), s, nil, true); err == nil {
		t.Error("resuming with nothing held did not error")
	}
}

// Confirmation is off by default, so nothing that already used the runner
// silently changed behaviour.
func TestConfirmationIsOptIn(t *testing.T) {
	s, planID := newAgentTestStore(t)
	r := NewRegistry()
	RegisterAll(r)

	gen := &scriptedGen{replies: []string{
		`{"tool":"move_meal","args":{"title":"chicken quesadillas","day":"monday"}}`,
		`{"say":"Done."}`,
	}}
	runner := &Runner{Gen: gen, Registry: r} // ConfirmMutations not set

	reply, err := runner.Run(context.Background(), s, nil, "move it")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply.Pending != nil {
		t.Fatal("a call was held with confirmation off")
	}
	if m := mealAt(t, s, planID, "2026-01-05", "dinner"); m == nil || m.Title != "Chicken Quesadillas" {
		t.Error("the move did not happen")
	}
}

func TestDescribeCall(t *testing.T) {
	tool := &Tool{
		Name:     "move_meal",
		Required: []string{"day"},
		Params: map[string]Param{
			"day": {Type: "string"}, "slot": {Type: "string"}, "meal_id": {Type: "integer"},
		},
	}

	got := describeCall(tool, json.RawMessage(`{"slot":"dinner","day":"monday","meal_id":47}`))
	// Required arguments lead: they are what the call is about.
	if !strings.HasPrefix(got, "day: monday") {
		t.Errorf("describeCall = %q, want it to lead with the required argument", got)
	}
	// An id must not read as "47.000000" - every JSON number decodes as a float.
	if !strings.Contains(got, "meal_id: 47") {
		t.Errorf("describeCall = %q, want a whole-number id", got)
	}

	if got := describeCall(tool, json.RawMessage(`{}`)); got != "no arguments" {
		t.Errorf("empty args = %q", got)
	}
	// Unparseable arguments still have to render something a reader can judge,
	// rather than an empty confirmation box.
	if got := describeCall(tool, json.RawMessage(`not json`)); got == "" {
		t.Error("unparseable args produced an empty description")
	}
}
