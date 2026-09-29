package plan

import (
	"context"
	"errors"
	"testing"

	"goeat/llm"
)

// cutOffGenerator is scriptedGenerator plus a per-call "hit the token limit"
// flag, recording the budget and prompt each call was given.
type cutOffGenerator struct {
	replies   []string
	truncated []bool
	calls     int
	prompts   []string
	maxTokens []int
}

func (g *cutOffGenerator) ProviderName() string { return "fake" }
func (g *cutOffGenerator) ModelName() string    { return "fake-model" }

func (g *cutOffGenerator) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	g.prompts = append(g.prompts, req.Prompt)
	g.maxTokens = append(g.maxTokens, req.MaxTokens)
	i := g.calls
	g.calls++
	return llm.GenerateResponse{Content: g.replies[i], Truncated: g.truncated[i]}, nil
}

// A reply cut off at the token limit must come back as a resumable
// *TruncatedError, and resuming it must re-send the exact same prompt with
// the new budget - without re-running the tool lookup that came before it.
func TestGenerate_TruncatedReplyResumesWithSamePrompt(t *testing.T) {
	ctx := context.Background()
	store, hhID := newGenerateTestStore(t)

	full := onePlan("Resumed")
	gen := &cutOffGenerator{
		replies:   []string{`{"tool": "read_pantry", "args": {}}`, full[:len(full)/2], full},
		truncated: []bool{false, true, false},
	}

	planID, err := Generate(ctx, store, gen, hhID, nil, nil, nil)
	var te *TruncatedError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TruncatedError", err)
	}
	if te.PlanID != planID || te.MaxTokens != llm.DefaultPlanMaxTokens {
		t.Fatalf("TruncatedError = %+v, want plan %d at %d tokens", te, planID, llm.DefaultPlanMaxTokens)
	}
	if p, _ := store.GetLatestPlan(ctx, hhID); p == nil || p.Status != "error" {
		t.Fatalf("plan status after cut-off = %v, want error", p)
	}

	gotID, err := ResumeGeneration(ctx, gen, te, 40000, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if gotID != planID {
		t.Errorf("resume finished plan %d, want the same plan %d", gotID, planID)
	}
	if gen.calls != 3 {
		t.Fatalf("model called %d times, want 3 (tool, cut-off, resumed) - the tool must not re-run", gen.calls)
	}
	if gen.prompts[2] != gen.prompts[1] {
		t.Error("resumed request's prompt differs from the cut-off one")
	}
	if gen.maxTokens[1] != llm.DefaultPlanMaxTokens || gen.maxTokens[2] != 40000 {
		t.Errorf("budgets = %v, want [.., %d, 40000]", gen.maxTokens, llm.DefaultPlanMaxTokens)
	}
	if p, _ := store.GetLatestPlan(ctx, hhID); p == nil || p.Status != "ready" {
		t.Fatalf("plan status after resume = %v, want ready", p)
	}
	if meals, _ := store.ListMealsByPlan(ctx, planID); len(meals) != 21 {
		t.Errorf("got %d meals, want 21", len(meals))
	}
}
