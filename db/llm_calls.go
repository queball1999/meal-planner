package db

import (
	"context"
	"fmt"
	"time"
)

// LLMCallKeep is how many llm_calls rows InsertLLMCall keeps. Plan-generation
// prompts carry the growing tool transcript, so rows can be tens of KB each.
const LLMCallKeep = 500

// LLMCall is one LLM Generate call as shown on the admin Audit Log.
type LLMCall struct {
	ID          int64
	HouseholdID int64 // 0 when the call ran outside a household
	PlanID      int64 // 0 unless the call ran for a plan generation
	Purpose     string
	Provider    string
	Model       string
	System      string
	Prompt      string
	Response    string
	Error       string
	DurationMS  int64
	InputToks   int
	OutputToks  int
	At          time.Time // when the call started; zero means now
}

// InsertLLMCall records one call and drops rows older than the newest
// LLMCallKeep.
func (s *store) InsertLLMCall(ctx context.Context, c LLMCall) error {
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	var hh any
	if c.HouseholdID != 0 {
		hh = c.HouseholdID
	}
	var planID any
	if c.PlanID != 0 {
		planID = c.PlanID
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_calls
		    (household_id, plan_id, purpose, provider, model, system, prompt, response,
		     error, duration_ms, input_tokens, output_tokens, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hh, planID, c.Purpose, c.Provider, c.Model, c.System, c.Prompt, c.Response,
		c.Error, c.DurationMS, c.InputToks, c.OutputToks,
		at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert llm call: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil && id > LLMCallKeep {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM llm_calls WHERE id <= ?`, id-LLMCallKeep); err != nil {
			return fmt.Errorf("prune llm calls: %w", err)
		}
	}
	return nil
}

const llmCallCols = `id, COALESCE(household_id, 0), COALESCE(plan_id, 0), purpose,
	provider, model, system, prompt, response, error, duration_ms, input_tokens,
	output_tokens, created_at`

// ListLLMCalls returns the most recent calls, newest first, capped at limit
// (defaulting to and capped at LLMCallKeep).
func (s *store) ListLLMCalls(ctx context.Context, limit int) ([]*LLMCall, error) {
	if limit <= 0 || limit > LLMCallKeep {
		limit = LLMCallKeep
	}
	return s.queryLLMCalls(ctx,
		`SELECT `+llmCallCols+` FROM llm_calls ORDER BY id DESC LIMIT ?`, limit)
}

// ListLLMCallsForPlan returns every kept call made for one plan's generation,
// newest first. householdID must own the plan's calls: another household's
// plan id returns nothing.
func (s *store) ListLLMCallsForPlan(ctx context.Context, householdID, planID int64) ([]*LLMCall, error) {
	return s.queryLLMCalls(ctx,
		`SELECT `+llmCallCols+` FROM llm_calls
		 WHERE plan_id = ? AND household_id = ? ORDER BY id DESC`, planID, householdID)
}

func (s *store) queryLLMCalls(ctx context.Context, query string, args ...any) ([]*LLMCall, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list llm calls: %w", err)
	}
	defer rows.Close()

	var out []*LLMCall
	for rows.Next() {
		var c LLMCall
		var at string
		if err := rows.Scan(&c.ID, &c.HouseholdID, &c.PlanID, &c.Purpose, &c.Provider,
			&c.Model, &c.System, &c.Prompt, &c.Response, &c.Error, &c.DurationMS,
			&c.InputToks, &c.OutputToks, &at); err != nil {
			return nil, fmt.Errorf("scan llm call: %w", err)
		}
		c.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, &c)
	}
	return out, rows.Err()
}
