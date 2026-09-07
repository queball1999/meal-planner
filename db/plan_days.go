package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// memberIDsJSON round-trips PlanDay.MemberIDs through the plan_days.member_ids
// TEXT column. A JSON array rather than a join table: the list is small, it is
// only ever read whole with its day, and it is deliberately a *snapshot* -
// deleting a household member must not silently rewrite which days they were
// counted on.
func memberIDsJSON(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// A row written before migration 00018, or by hand, can hold "" as easily as
// "[]"; both mean "nobody chosen", and neither is an error worth failing a
// page render over.
func parseMemberIDs(raw string) []int64 {
	if raw == "" || raw == "[]" {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

func (s *store) ListPlanDays(ctx context.Context, planID int64) ([]*PlanDay, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, plan_id, date, headcount, note, member_ids, portions, status
		FROM plan_days WHERE plan_id = ?
		ORDER BY date`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PlanDay
	for rows.Next() {
		var d PlanDay
		var memberIDs string
		if err := rows.Scan(&d.ID, &d.PlanID, &d.Date, &d.Headcount, &d.Note, &memberIDs, &d.Portions, &d.Status); err != nil {
			return nil, err
		}
		d.MemberIDs = parseMemberIDs(memberIDs)
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *store) UpsertPlanDay(ctx context.Context, p UpsertPlanDayParams) error {
	// A caller that knows nothing about members (plan generation, the legacy
	// headcount form) sends Portions 0; that is Headcount standard portions,
	// which is exactly the behaviour those callers had before members existed.
	portions := p.Portions
	if portions <= 0 {
		portions = float64(p.Headcount)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plan_days (plan_id, date, headcount, note, member_ids, portions)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(plan_id, date) DO UPDATE SET
		  headcount  = excluded.headcount,
		  note       = excluded.note,
		  member_ids = excluded.member_ids,
		  portions   = excluded.portions`,
		p.PlanID, p.Date, p.Headcount, p.Note, memberIDsJSON(p.MemberIDs), portions,
	)
	return err
}

func (s *store) GetPlanDay(ctx context.Context, planID int64, date string) (*PlanDay, error) {
	var d PlanDay
	var memberIDs string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, plan_id, date, headcount, note, member_ids, portions, status
		FROM plan_days WHERE plan_id = ? AND date = ?`, planID, date).
		Scan(&d.ID, &d.PlanID, &d.Date, &d.Headcount, &d.Note, &memberIDs, &d.Portions, &d.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.MemberIDs = parseMemberIDs(memberIDs)
	return &d, nil
}

// ScaleMealsForDay rewrites every non-leftover meal on one day of a plan to
// serve `portions` adult-equivalent servings: servings, cooked portions, the
// recipe's stated yield, and every ingredient quantity are all recomputed as
// base × portions / base_servings.
//
// Portions rather than a headcount because a household is people, not a
// number: two adults and two toddlers is 3.0 portions, not 4 (see
// household_members.portion_factor). A caller with no member data passes the
// headcount, which is that many standard portions and behaves exactly as
// before.
//
// Scaling from the as-generated base (00017) rather than from the current
// values is what makes this idempotent - 2 → 6 → 3 people lands on exactly the
// numbers you would get going straight to 3, with no compounding rounding
// error. Leftover slots are skipped: they carry no ingredients of their own and
// their portions come from the meal that cooked them.
//
// The whole day moves in one transaction, so a failure part-way cannot leave a
// day with half-scaled recipes.
func (s *store) ScaleMealsForDay(ctx context.Context, planID int64, date string, portions float64) (ScaleDayResult, error) {
	var res ScaleDayResult
	if portions < 1 {
		return res, errors.New("portions must be at least 1")
	}

	// Ingredient quantities scale by the raw portion total, so two adults and
	// two toddlers (3.0) really do buy three servings' worth of food. The
	// integer columns - servings, cooked_portions - take the rounded value,
	// because "2.6 servings" is not a thing to print on a recipe card.
	servings := int(math.Round(portions))
	if servings < 1 {
		servings = 1
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
		factor := portions / float64(t.baseServings)

		// Cooked portions keep their generated surplus ratio (a batch-cook meal
		// that made 1.5× its servings still does after scaling), but can never
		// drop below what the people eating actually need.
		cooked := int(math.Round(float64(t.baseCookedPortions) * factor))
		if cooked < servings {
			cooked = servings
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE meals SET servings = ?, cooked_portions = ? WHERE id = ?`,
			servings, cooked, t.id); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE meal_recipes SET servings = ? WHERE meal_id = ?`,
			servings, t.id); err != nil {
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

// SetPlanDayStatus marks a day cooking / eating out / skipped.
//
// Deliberately separate from UpsertPlanDay rather than a field on it: the
// upsert runs on plan generation and on every headcount change, and a status
// carried on those params would be "cooking" by default at each of those call
// sites, silently un-marking a day the user had marked eating out.
//
// The row is created if the day has none, so a status can be set on a plan
// generated before this column existed.
func (s *store) SetPlanDayStatus(ctx context.Context, planID int64, date, status string) error {
	if !ValidDayStatus(status) {
		return fmt.Errorf("unknown day status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plan_days (plan_id, date, headcount, status)
		VALUES (?, ?, 0, ?)
		ON CONFLICT(plan_id, date) DO UPDATE SET status = excluded.status`,
		planID, date, status)
	return err
}

// ListLeftoversSourcedFrom returns the meals that eat leftovers cooked on the
// given day - the meals that lose their food if that day stops being cooked.
//
// This is the dependency the day-status dialog has to surface: marking Tuesday
// "eating out" silently empties Wednesday's dinner if Wednesday was living off
// Tuesday's batch, and a plan that quietly loses a meal is worse than one that
// asks.
func (s *store) ListLeftoversSourcedFrom(ctx context.Context, planID int64, date string) ([]*Meal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.plan_id, m.day, m.slot, m.title, m.effort, m.servings,
		       m.cooked_portions, m.is_leftover, m.leftover_source_meal_id,
		       m.locked, m.ai_run_id
		FROM meals m
		JOIN meals src ON src.id = m.leftover_source_meal_id
		WHERE m.plan_id = ? AND m.is_leftover = 1 AND src.day = ?
		ORDER BY m.day, m.slot`, planID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Meal
	for rows.Next() {
		var m Meal
		var isLeftover int
		if err := rows.Scan(&m.ID, &m.PlanID, &m.Day, &m.Slot, &m.Title, &m.Effort,
			&m.Servings, &m.CookedPortions, &isLeftover, &m.LeftoverSourceMealID,
			&m.Locked, &m.AIRunID); err != nil {
			return nil, err
		}
		m.IsLeftover = isLeftover != 0
		out = append(out, &m)
	}
	return out, rows.Err()
}
