package db

import (
	"context"
	"database/sql"
	"encoding/json"
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

// SetPlanOnHand records the food the household said it already has when it
// generated this plan, so every later shopping-list build can honour it.
func (s *store) SetPlanOnHand(ctx context.Context, planID int64, names []string) error {
	if names == nil {
		names = []string{}
	}
	raw, err := json.Marshal(names)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE plans SET on_hand = ? WHERE id = ?`, string(raw), planID)
	return err
}

// GetPlanOnHand returns SetPlanOnHand's list; empty (not an error) for a plan
// generated before it existed or without one.
func (s *store) GetPlanOnHand(ctx context.Context, planID int64) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT on_hand FROM plans WHERE id = ?`, planID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &names); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// SetPlanRequest records the generate dialog's submission on the plan it
// produced (00041), so it can be shown later and sent again for another week.
func (s *store) SetPlanRequest(ctx context.Context, planID int64, req PlanRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE plans SET gen_request = ? WHERE id = ?`, string(raw), planID)
	return err
}

// GetPlanRequest returns SetPlanRequest's submission, or nil (not an error)
// for a plan that has none - built by hand, by the scheduler, or before 00041.
func (s *store) GetPlanRequest(ctx context.Context, planID int64) (*PlanRequest, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT gen_request FROM plans WHERE id = ?`, planID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && raw == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var req PlanRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// ListPlanRequests returns the household's most recent saved requests, newest
// first - the generate dialog's "reuse an earlier request" list. Canceled
// plans are included: a request is worth reusing whether or not the plan it
// made was later regenerated.
func (s *store) ListPlanRequests(ctx context.Context, householdID int64, limit int) ([]*StoredPlanRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, week_start, week_end, created_at, gen_request
		FROM plans
		WHERE household_id = ? AND gen_request != ''
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, householdID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*StoredPlanRequest
	for rows.Next() {
		var r StoredPlanRequest
		var createdAt, raw string
		if err := rows.Scan(&r.PlanID, &r.WeekStart, &r.WeekEnd, &createdAt, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &r.Request); err != nil {
			continue // one unreadable row must not hide the rest
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *store) UpdatePlanStatus(ctx context.Context, planID int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE plans SET status = ? WHERE id = ?`, status, planID)
	return err
}

// DeletePlan removes a plan and (via ON DELETE CASCADE) its meals, recipes,
// ingredients, plan days, and price runs. Scoped by household so one household
// can't delete another's plan.
func (s *store) DeletePlan(ctx context.Context, householdID, planID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM plans WHERE id = ? AND household_id = ?`, planID, householdID)
	return err
}

// DeleteAllPlansForHousehold wipes every plan - past, present, ready, error,
// or canceled - for a household (Settings → Danger zone). Cascades to plan
// days, meals, meal recipes, meal ingredients, and shopping list items.
// Unlike a regenerate's soft-cancel, this is permanent: nothing is kept for
// /plan/history.
func (s *store) DeleteAllPlansForHousehold(ctx context.Context, householdID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM plans WHERE household_id = ?`, householdID)
	return err
}

func (s *store) GetLatestPlan(ctx context.Context, householdID int64) (*Plan, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, week_start, week_end, budget_cents, total_cents,
		       confidence_summary, status, canceled, created_at
		FROM plans
		WHERE household_id = ? AND canceled = 0
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
		       confidence_summary, status, canceled, created_at
		FROM plans WHERE id = ?`, planID)
	return scanPlan(row)
}

// CancelOtherPlansForWeek flags every other non-canceled plan for a household
// and week as canceled, keeping exactly keepPlanID as the active one. Called
// once a regenerated plan is confirmed ready, so a failed regenerate leaves
// the old plan untouched and active (§ plan.Generate). Canceled plans are
// never deleted - they stay visible in /plan/history for archival purposes.
func (s *store) CancelOtherPlansForWeek(ctx context.Context, householdID int64, weekStart string, keepPlanID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE plans SET canceled = 1
		WHERE household_id = ? AND week_start = ? AND id != ? AND canceled = 0`,
		householdID, weekStart, keepPlanID)
	return err
}

func (s *store) UpdatePlanTotal(ctx context.Context, planID int64, totalCents int64, confidenceSummary string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE plans SET total_cents = ?, confidence_summary = ? WHERE id = ?`,
		totalCents, confidenceSummary, planID)
	return err
}

func (s *store) ListPlans(ctx context.Context, householdID int64) ([]*Plan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, week_start, week_end, budget_cents, total_cents,
		       confidence_summary, status, canceled, created_at
		FROM plans
		WHERE household_id = ?
		ORDER BY created_at DESC`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlans(rows)
}

func (s *store) ListPlansInRange(ctx context.Context, householdID int64, from, to string) ([]*Plan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, week_start, week_end, budget_cents, total_cents,
		       confidence_summary, status, canceled, created_at
		FROM plans
		WHERE household_id = ? AND week_start >= ? AND week_start <= ?
		ORDER BY week_start DESC`, householdID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlans(rows)
}

// GetPlanByWeekStart returns the active (non-canceled) plan for a week, or
// nil, nil if that week's plan was regenerated-away or never existed. Use
// GetPlanByID to look up a specific canceled/archived plan directly.
func (s *store) GetPlanByWeekStart(ctx context.Context, householdID int64, weekStart string) (*Plan, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, week_start, week_end, budget_cents, total_cents,
		       confidence_summary, status, canceled, created_at
		FROM plans
		WHERE household_id = ? AND week_start = ? AND canceled = 0
		ORDER BY created_at DESC
		LIMIT 1`, householdID, weekStart)
	return scanPlan(row)
}

func scanPlans(rows *sql.Rows) ([]*Plan, error) {
	var plans []*Plan
	for rows.Next() {
		var p Plan
		var createdAt string
		var canceled int
		if err := rows.Scan(
			&p.ID, &p.HouseholdID, &p.WeekStart, &p.WeekEnd,
			&p.BudgetCents, &p.TotalCents, &p.ConfidenceSummary,
			&p.Status, &canceled, &createdAt,
		); err != nil {
			return nil, err
		}
		p.Canceled = canceled != 0
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		plans = append(plans, &p)
	}
	return plans, rows.Err()
}

func scanPlan(row *sql.Row) (*Plan, error) {
	var p Plan
	var createdAt string
	var canceled int
	err := row.Scan(
		&p.ID, &p.HouseholdID, &p.WeekStart, &p.WeekEnd,
		&p.BudgetCents, &p.TotalCents, &p.ConfidenceSummary,
		&p.Status, &canceled, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Canceled = canceled != 0
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &p, nil
}
