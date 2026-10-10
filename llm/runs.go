package llm

import (
	"context"
	"log"
	"time"

	"goeat/db"
)

// RunStore is the slice of db.Store the recorder needs.
type RunStore interface {
	CreateAIRun(ctx context.Context, p db.CreateAIRunParams) (*db.AIRun, error)
}

type runCtxKey int

const (
	ctxKeyRunHousehold runCtxKey = iota
	ctxKeyRunPurpose
	ctxKeyRunPlan
)

// WithHousehold tags ctx with the household every LLM call made under it is
// billed to. ai_runs.household_id is NOT NULL, so a call with no household
// in its context is not recorded. The web middleware sets this for every
// signed-in request; background jobs set it themselves.
func WithHousehold(ctx context.Context, householdID int64) context.Context {
	return context.WithValue(ctx, ctxKeyRunHousehold, householdID)
}

// WithPurpose tags ctx with what the LLM calls under it are for ("plan",
// "repair", "price_estimate", ...) - the finance page's "by purpose" rollup.
// The innermost tag wins.
func WithPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, ctxKeyRunPurpose, purpose)
}

// WithPlan tags ctx with the plan whose generation the LLM calls under it
// belong to, so the call log can show one generation's calls on their own.
func WithPlan(ctx context.Context, planID int64) context.Context {
	return context.WithValue(ctx, ctxKeyRunPlan, planID)
}

// runRecorder wraps a Generator and writes one ai_runs row per Generate call,
// success or failure, with that call's own token usage.
type runRecorder struct {
	inner Generator
	store RunStore
}

// NewRunRecorder wraps gen so every call is recorded in ai_runs. Every
// caller gets per-call usage accounting without doing it by hand.
func NewRunRecorder(gen Generator, store RunStore) Generator {
	return &runRecorder{inner: gen, store: store}
}

func (r *runRecorder) ProviderName() string { return r.inner.ProviderName() }
func (r *runRecorder) ModelName() string    { return r.inner.ModelName() }
func (r *runRecorder) PlanMaxTokens() int   { return PlanMaxTokens(r.inner) }

func (r *runRecorder) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	resp, err := r.inner.Generate(ctx, req)

	hhID, _ := ctx.Value(ctxKeyRunHousehold).(int64)
	if hhID == 0 {
		return resp, err
	}
	purpose, _ := ctx.Value(ctxKeyRunPurpose).(string)
	if purpose == "" {
		purpose = "other"
	}
	status := "ok"
	switch {
	case err != nil:
		status = "error"
	case resp.Truncated:
		status = "truncated"
	}
	model := r.inner.ModelName()
	if resp.ModelName != "" {
		model = resp.ModelName
	}

	// Detached: a call that failed because ctx was canceled or timed out
	// still spent tokens and still deserves its row.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	run, rerr := r.store.CreateAIRun(wctx, db.CreateAIRunParams{
		HouseholdID:      hhID,
		Purpose:          purpose,
		Provider:         r.inner.ProviderName(),
		Model:            model,
		PromptTokens:     resp.InputTokens,
		CompletionTokens: resp.OutputTokens,
		Status:           status,
	})
	if rerr != nil {
		log.Printf("llm: record ai run: %v", rerr)
	} else if err == nil {
		resp.RunID = run.ID
	}
	return resp, err
}
