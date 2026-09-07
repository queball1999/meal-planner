package db

import (
	"context"
	"database/sql"
	"errors"
	"math"
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

// ScaleMealsForDay rewrites every non-leftover meal on one day of a plan to
// serve headcount people: servings, cooked portions, the recipe's stated yield,
// and every ingredient quantity are all recomputed as
// base × headcount / base_servings.
//
// Scaling from the as-generated base (00017) rather than from the current
// values is what makes this idempotent - 2 → 6 → 3 people lands on exactly the
// numbers you would get going straight to 3, with no compounding rounding
// error. Leftover slots are skipped: they carry no ingredients of their own and
// their portions come from the meal that cooked them.
//
// The whole day moves in one transaction, so a failure part-way cannot leave a
// day with half-scaled recipes.
func (s *store) ScaleMealsForDay(ctx context.Context, planID int64, date string, headcount int) (ScaleDayResult, error) {
	var res ScaleDayResult
	if headcount < 1 {
		return res, errors.New("headcount must be at least 1")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT id,
		       CASE WHEN base_servings        > 0 THEN base_servings        ELSE servings        END,
		       CASE WHEN base_cooked_portions > 0 THEN base_cooked_portions ELSE cooked_portions END
		FROM meals
		WHERE plan_id = ? AND day = ? AND is_leftover = 0`, planID, date)
	if err != nil {
		return res, err
	}
	type target struct {
		id                 int64
		baseServings       int
		baseCookedPortions int
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.baseServings, &t.baseCookedPortions); err != nil {
			rows.Close()
			return res, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	for _, t := range targets {
		if t.baseServings < 1 {
			continue // nothing sane to scale from
		}
		factor := float64(headcount) / float64(t.baseServings)

		// Cooked portions keep their generated surplus ratio (a batch-cook meal
		// that made 1.5× its servings still does after scaling), but can never
		// drop below the number of people actually eating.
		cooked := int(math.Round(float64(t.baseCookedPortions) * factor))
		if cooked < headcount {
			cooked = headcount
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE meals SET servings = ?, cooked_portions = ? WHERE id = ?`,
			headcount, cooked, t.id); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE meal_recipes SET servings = ? WHERE meal_id = ?`,
			headcount, t.id); err != nil {
			return res, err
		}

		// Round to 3 decimals: enough precision for "0.375 cup" without
		// persisting float noise into the shopping-list maths.
		ir, err := tx.ExecContext(ctx, `
			UPDATE meal_ingredients
			SET quantity = ROUND(base_quantity * ?, 3)
			WHERE meal_id = ? AND base_quantity > 0`, factor, t.id)
		if err != nil {
			return res, err
		}
		n, _ := ir.RowsAffected()
		res.IngredientsScaled += int(n)
		res.MealsScaled++
	}

	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}
