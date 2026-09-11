package db

import (
	"context"
	"fmt"
	"time"
)

func (s *store) CreateAIRun(ctx context.Context, p CreateAIRunParams) (*AIRun, error) {
	status := p.Status
	if status == "" {
		status = "ok"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ai_runs
		   (household_id, purpose, provider, model, prompt_tokens, completion_tokens, est_cost_cents, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.HouseholdID, p.Purpose, p.Provider, p.Model,
		p.PromptTokens, p.CompletionTokens, p.EstCostCents, status)
	if err != nil {
		return nil, fmt.Errorf("create ai run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create ai run last id: %w", err)
	}

	row := s.db.QueryRowContext(ctx,
		`SELECT id, household_id, purpose, provider, model,
		        prompt_tokens, completion_tokens, est_cost_cents, status, created_at
		   FROM ai_runs WHERE id = ?`, id)
	return scanAIRun(row)
}

func scanAIRun(sc storeScanner) (*AIRun, error) {
	var r AIRun
	var createdAt string
	if err := sc.Scan(&r.ID, &r.HouseholdID, &r.Purpose, &r.Provider, &r.Model,
		&r.PromptTokens, &r.CompletionTokens, &r.EstCostCents, &r.Status, &createdAt); err != nil {
		return nil, fmt.Errorf("scan ai run: %w", err)
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &r, nil
}

// aiRunRange is the shared WHERE clause for the finance aggregations. created_at
// is stored as RFC3339Nano; date(created_at) yields a YYYY-MM-DD that compares
// cleanly against the from/to strings the handlers pass in.
const aiRunRange = `household_id = ? AND date(created_at) >= ? AND date(created_at) <= ?`

// GetAIRunTotals aggregates usage and cost over a date range.
func (s *store) GetAIRunTotals(ctx context.Context, householdID int64, from, to string) (*AIRunTotals, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(prompt_tokens + completion_tokens), 0),
			COALESCE(SUM(est_cost_cents), 0),
			COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0)
		FROM ai_runs
		WHERE `+aiRunRange,
		householdID, from, to,
	)
	var t AIRunTotals
	if err := row.Scan(&t.RunCount, &t.PromptTokens, &t.CompletionTokens,
		&t.TotalTokens, &t.EstCostCents, &t.ErrorCount); err != nil {
		return &AIRunTotals{}, nil
	}
	return &t, nil
}

// ListAIRunsByPurpose rolls usage/cost up per purpose (the "by purpose" table).
func (s *store) ListAIRunsByPurpose(ctx context.Context, householdID int64, from, to string) ([]*AIRunByPurpose, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT purpose,
			COUNT(*),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(prompt_tokens + completion_tokens), 0),
			COALESCE(SUM(est_cost_cents), 0)
		FROM ai_runs
		WHERE `+aiRunRange+`
		GROUP BY purpose
		ORDER BY SUM(est_cost_cents) DESC, purpose ASC`,
		householdID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AIRunByPurpose
	for rows.Next() {
		var r AIRunByPurpose
		if err := rows.Scan(&r.Purpose, &r.RunCount, &r.PromptTokens,
			&r.CompletionTokens, &r.TotalTokens, &r.EstCostCents); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListAIRunsByModel rolls usage/cost up per (provider, model) - the "by model"
// table. This is the set of local labels that need a pricing mapping.
func (s *store) ListAIRunsByModel(ctx context.Context, householdID int64, from, to string) ([]*AIRunByModel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider, model,
			COUNT(*),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(prompt_tokens + completion_tokens), 0),
			COALESCE(SUM(est_cost_cents), 0)
		FROM ai_runs
		WHERE `+aiRunRange+`
		GROUP BY provider, model
		ORDER BY SUM(est_cost_cents) DESC, provider ASC, model ASC`,
		householdID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AIRunByModel
	for rows.Next() {
		var r AIRunByModel
		if err := rows.Scan(&r.Provider, &r.Model, &r.RunCount, &r.PromptTokens,
			&r.CompletionTokens, &r.TotalTokens, &r.EstCostCents); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListAIRunsDaily returns one row per day in the range (quiet days included as
// zeros) for the trend chart. The handlers fill the gaps; this returns only the
// days that have data, ordered oldest first.
func (s *store) ListAIRunsDaily(ctx context.Context, householdID int64, from, to string) ([]*AIRunDaily, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(created_at),
			COUNT(*),
			COALESCE(SUM(prompt_tokens + completion_tokens), 0),
			COALESCE(SUM(est_cost_cents), 0)
		FROM ai_runs
		WHERE `+aiRunRange+`
		GROUP BY date(created_at)
		ORDER BY date(created_at) ASC`,
		householdID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AIRunDaily
	for rows.Next() {
		var r AIRunDaily
		if err := rows.Scan(&r.Date, &r.RunCount, &r.TotalTokens, &r.EstCostCents); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListAIRuns returns the most recent individual runs (the audit-style table).
func (s *store) ListAIRuns(ctx context.Context, householdID int64, limit int) ([]*AIRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, purpose, provider, model,
		       prompt_tokens, completion_tokens, est_cost_cents, status, created_at
		FROM ai_runs
		WHERE household_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, householdID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AIRun
	for rows.Next() {
		r, err := scanAIRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
