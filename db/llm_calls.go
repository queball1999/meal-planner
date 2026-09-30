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
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_calls
		    (household_id, purpose, provider, model, system, prompt, response,
		     error, duration_ms, input_tokens, output_tokens, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hh, c.Purpose, c.Provider, c.Model, c.System, c.Prompt, c.Response,
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

// ListLLMCalls returns the most recent calls, newest first, capped at limit
// (defaulting to and capped at LLMCallKeep).
func (s *store) ListLLMCalls(ctx context.Context, limit int) ([]*LLMCall, error) {
	if limit <= 0 || limit > LLMCallKeep {
		limit = LLMCallKeep
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(household_id, 0), purpose, provider, model, system,
		       prompt, response, error, duration_ms, input_tokens, output_tokens,
		       created_at
		FROM llm_calls ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list llm calls: %w", err)
	}
	defer rows.Close()

	var out []*LLMCall
	for rows.Next() {
		var c LLMCall
		var at string
		if err := rows.Scan(&c.ID, &c.HouseholdID, &c.Purpose, &c.Provider, &c.Model,
			&c.System, &c.Prompt, &c.Response, &c.Error, &c.DurationMS,
			&c.InputToks, &c.OutputToks, &at); err != nil {
			return nil, fmt.Errorf("scan llm call: %w", err)
		}
		c.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, &c)
	}
	return out, rows.Err()
}
