package llm

import (
	"context"
	"log"
	"time"

	"goeat/db"
)

// CallLogStore is the slice of db.Store the call logger needs.
type CallLogStore interface {
	InsertLLMCall(ctx context.Context, c db.LLMCall) error
}

// debugLogger wraps a Generator, writing every call - prompt, response,
// error - to llm_calls for the admin Audit Log. The log lives in the
// database, not in this wrapper, so rebuilding the generator on a settings
// save or a restart loses nothing, and a busy plan run cannot push its own
// calls out.
type debugLogger struct {
	inner Generator
	store CallLogStore
}

// NewDebugLogger wraps gen so every call is written to the Audit Log.
func NewDebugLogger(gen Generator, store CallLogStore) Generator {
	return &debugLogger{inner: gen, store: store}
}

func (d *debugLogger) ProviderName() string { return d.inner.ProviderName() }
func (d *debugLogger) ModelName() string    { return d.inner.ModelName() }
func (d *debugLogger) PlanMaxTokens() int   { return PlanMaxTokens(d.inner) }

func (d *debugLogger) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	start := time.Now()
	resp, err := d.inner.Generate(ctx, req)

	hhID, _ := ctx.Value(ctxKeyRunHousehold).(int64)
	purpose, _ := ctx.Value(ctxKeyRunPurpose).(string)
	planID, _ := ctx.Value(ctxKeyRunPlan).(int64)
	c := db.LLMCall{
		HouseholdID: hhID,
		PlanID:      planID,
		Purpose:     purpose,
		Provider:    d.inner.ProviderName(),
		Model:       d.inner.ModelName(),
		System:      req.System,
		Prompt:      req.Prompt,
		DurationMS:  time.Since(start).Milliseconds(),
		At:          start,
	}
	if err != nil {
		c.Error = err.Error()
	}
	// A truncated or failed call can still carry partial content; keep it,
	// since that is what explains the failure.
	c.Response = resp.Content
	c.InputToks = resp.InputTokens
	c.OutputToks = resp.OutputTokens
	if resp.ModelName != "" {
		c.Model = resp.ModelName
	}

	// Detached: a call cut short by a canceled request still belongs in the log.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if lerr := d.store.InsertLLMCall(wctx, c); lerr != nil {
		log.Printf("llm: record call log: %v", lerr)
	}
	return resp, err
}
