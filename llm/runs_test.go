package llm

import (
	"context"
	"errors"
	"testing"

	"goeat/db"
)

type stubGen struct {
	resp GenerateResponse
	err  error
}

func (s stubGen) ProviderName() string { return "stub" }
func (s stubGen) ModelName() string    { return "stub-model" }
func (s stubGen) Generate(context.Context, GenerateRequest) (GenerateResponse, error) {
	return s.resp, s.err
}

type memRuns struct{ runs []db.CreateAIRunParams }

func (m *memRuns) CreateAIRun(_ context.Context, p db.CreateAIRunParams) (*db.AIRun, error) {
	m.runs = append(m.runs, p)
	return &db.AIRun{ID: int64(len(m.runs))}, nil
}

func TestRunRecorder_OneRowPerCall(t *testing.T) {
	store := &memRuns{}
	ctx := WithPurpose(WithHousehold(context.Background(), 7), "plan")

	ok := NewRunRecorder(stubGen{resp: GenerateResponse{InputTokens: 100, OutputTokens: 50}}, store)
	resp, _ := ok.Generate(ctx, GenerateRequest{})
	cut := NewRunRecorder(stubGen{resp: GenerateResponse{OutputTokens: 16384, Truncated: true}}, store)
	_, _ = cut.Generate(ctx, GenerateRequest{})
	bad := NewRunRecorder(stubGen{err: errors.New("boom")}, store)
	_, _ = bad.Generate(WithPurpose(ctx, "repair"), GenerateRequest{})

	if len(store.runs) != 3 {
		t.Fatalf("recorded %d runs, want 3", len(store.runs))
	}
	if resp.RunID != 1 {
		t.Errorf("RunID = %d, want 1", resp.RunID)
	}
	want := []struct {
		purpose, status string
		in, out         int
	}{{"plan", "ok", 100, 50}, {"plan", "truncated", 0, 16384}, {"repair", "error", 0, 0}}
	for i, w := range want {
		r := store.runs[i]
		if r.HouseholdID != 7 || r.Purpose != w.purpose || r.Status != w.status ||
			r.PromptTokens != w.in || r.CompletionTokens != w.out {
			t.Errorf("run %d = %+v, want %+v", i, r, w)
		}
	}
}

func TestRunRecorder_SkipsWithoutHousehold(t *testing.T) {
	store := &memRuns{}
	gen := NewRunRecorder(stubGen{}, store)
	if _, err := gen.Generate(context.Background(), GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(store.runs) != 0 {
		t.Errorf("recorded %d runs with no household in ctx, want 0", len(store.runs))
	}
}
