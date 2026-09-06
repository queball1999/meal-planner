package db

import (
	"context"
	"database/sql"
	"errors"
)

func (s *store) ListPlanDays(ctx context.Context, planID int64) ([]*PlanDay, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, plan_id, date, headcount, note
		FROM plan_days WHERE plan_id = ?
		ORDER BY date`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PlanDay
	for rows.Next() {
		var d PlanDay
		if err := rows.Scan(&d.ID, &d.PlanID, &d.Date, &d.Headcount, &d.Note); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *store) UpsertPlanDay(ctx context.Context, p UpsertPlanDayParams) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plan_days (plan_id, date, headcount, note)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(plan_id, date) DO UPDATE SET
		  headcount = excluded.headcount,
		  note      = excluded.note`,
		p.PlanID, p.Date, p.Headcount, p.Note,
	)
	return err
}

func (s *store) GetPlanDay(ctx context.Context, planID int64, date string) (*PlanDay, error) {
	var d PlanDay
	err := s.db.QueryRowContext(ctx, `
		SELECT id, plan_id, date, headcount, note
		FROM plan_days WHERE plan_id = ? AND date = ?`, planID, date).
		Scan(&d.ID, &d.PlanID, &d.Date, &d.Headcount, &d.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
