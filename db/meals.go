package db

import (
	"context"
	"database/sql"
	"errors"
)

func (s *store) CreateMeal(ctx context.Context, p CreateMealParams) (*Meal, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO meals
		  (plan_id, day, slot, title, effort, servings, cooked_portions, ai_run_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PlanID, p.Day, p.Slot, p.Title, p.Effort,
		p.Servings, p.CookedPortions, p.AIRunID,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.getMealByID(ctx, id)
}

func (s *store) ListMealsByPlan(ctx context.Context, planID int64) ([]*Meal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, plan_id, day, slot, title, effort, servings, cooked_portions,
		       is_leftover, leftover_source_meal_id, locked, ai_run_id
		FROM meals WHERE plan_id = ?
		ORDER BY day, CASE slot WHEN 'breakfast' THEN 0 WHEN 'lunch' THEN 1 ELSE 2 END`,
		planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Meal
	for rows.Next() {
		m, err := scanMeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *store) UpdateMealLocked(ctx context.Context, mealID int64, locked bool) error {
	v := 0
	if locked {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE meals SET locked = ? WHERE id = ?`, v, mealID)
	return err
}

func (s *store) GetMealByID(ctx context.Context, mealID int64) (*Meal, error) {
	return s.getMealByID(ctx, mealID)
}

func (s *store) UpdateMealLeftover(ctx context.Context, mealID int64, isLeftover bool, sourceMealID *int64) error {
	v := 0
	if isLeftover {
		v = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE meals SET is_leftover = ?, leftover_source_meal_id = ? WHERE id = ?`,
		v, sourceMealID, mealID,
	)
	return err
}

func (s *store) getMealByID(ctx context.Context, mealID int64) (*Meal, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, plan_id, day, slot, title, effort, servings, cooked_portions,
		       is_leftover, leftover_source_meal_id, locked, ai_run_id
		FROM meals WHERE id = ?`, mealID)
	return scanMealRow(row)
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanMeal(sc scanner) (*Meal, error) {
	var m Meal
	var isLeftover int
	var locked int
	err := sc.Scan(
		&m.ID, &m.PlanID, &m.Day, &m.Slot, &m.Title, &m.Effort,
		&m.Servings, &m.CookedPortions,
		&isLeftover, &m.LeftoverSourceMealID,
		&locked, &m.AIRunID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.IsLeftover = isLeftover == 1
	m.Locked = locked == 1
	return &m, nil
}

func scanMealRow(row *sql.Row) (*Meal, error) {
	var m Meal
	var isLeftover, locked int
	err := row.Scan(
		&m.ID, &m.PlanID, &m.Day, &m.Slot, &m.Title, &m.Effort,
		&m.Servings, &m.CookedPortions,
		&isLeftover, &m.LeftoverSourceMealID,
		&locked, &m.AIRunID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.IsLeftover = isLeftover == 1
	m.Locked = locked == 1
	return &m, nil
}
