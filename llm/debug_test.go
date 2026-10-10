package llm

import (
	"context"
	"errors"
	"testing"

	"goeat/db"
)

type memCalls struct{ calls []db.LLMCall }

func (m *memCalls) InsertLLMCall(_ context.Context, c db.LLMCall) error {
	m.calls = append(m.calls, c)
	return nil
}

// Every call lands in the log - success, failure, and one whose request was
// already canceled - with the prompt, reply and context tags.
func TestDebugLoggerRecordsEveryCall(t *testing.T) {
	store := &memCalls{}
	ctx := WithPlan(WithPurpose(WithHousehold(context.Background(), 7), "chat"), 3)

	ok := NewDebugLogger(stubGen{resp: GenerateResponse{Content: `{"say":"hi"}`, InputTokens: 10}}, store)
	_, _ = ok.Generate(ctx, GenerateRequest{System: "sys", Prompt: "hello"})

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	bad := NewDebugLogger(stubGen{err: errors.New("boom")}, store)
	_, _ = bad.Generate(canceled, GenerateRequest{Prompt: "again"})

	if len(store.calls) != 2 {
		t.Fatalf("logged %d calls, want 2", len(store.calls))
	}
	c := store.calls[0]
	if c.Prompt != "hello" || c.System != "sys" || c.Response != `{"say":"hi"}` ||
		c.HouseholdID != 7 || c.PlanID != 3 || c.Purpose != "chat" || c.InputToks != 10 || c.At.IsZero() {
		t.Errorf("first call = %+v", c)
	}
	if store.calls[1].Error != "boom" || store.calls[1].Prompt != "again" {
		t.Errorf("failed call = %+v", store.calls[1])
	}
}
