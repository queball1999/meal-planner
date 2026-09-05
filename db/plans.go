package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *store) CreatePlan(ctx context.Context, p CreatePlanParams) (*Plan, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO plans (household_id, week_start, week_end, budget_cents, status)
		VALUES (?, ?, ?, ?, 'generating')`,
		p.HouseholdID, p.WeekStart, p.WeekEnd, p.BudgetCents,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.getPlanByID(ctx, id)
}

func (s *store) UpdatePlanStatus(ctx context.Context, planID int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE plans SET status = ? WHERE id = ?`, status, planID)
	return err
}

func (s *store) GetLatestPlan(ctx context.Context, householdID int64) (*Plan, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, week_start, week_end, budget_cents, total_cents,
		       confidence_summary, status, created_at
		FROM plans
		WHERE household_id = ?
		ORDER BY created_at DESC
		LIMIT 1`, householdID)
	return scanPlan(row)
}

func (s *store) GetPlanByID(ctx context.Context, planID int64) (*Plan, error) {
	return s.getPlanByID(ctx, planID)
}

func (s *store) getPlanByID(ctx context.Context, planID int64) (*Plan, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, week_start, week_end, budget_cents, total_cents,
		       confidence_summary, status, created_at
		FROM plans WHERE id = ?`, planID)
	return scanPlan(row)
}

func scanPlan(row *sql.Row) (*Plan, error) {
	var p Plan
	var createdAt string
	err := row.Scan(
		&p.ID, &p.HouseholdID, &p.WeekStart, &p.WeekEnd,
		&p.BudgetCents, &p.TotalCents, &p.ConfidenceSummary,
		&p.Status, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &p, nil
}
