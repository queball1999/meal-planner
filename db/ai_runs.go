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
