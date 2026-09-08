package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *store) CreateMeal(ctx context.Context, p CreateMealParams) (*Meal, error) {
	// A freshly created meal is by definition unscaled, so its base yield is
	// whatever it was generated with (00017). Everything that later rescales a
	// day's portions reads these columns, never the live ones.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO meals
		  (plan_id, day, slot, title, effort, servings, cooked_portions,
		   base_servings, base_cooked_portions, ai_run_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PlanID, p.Day, p.Slot, p.Title, p.Effort,
		p.Servings, p.CookedPortions,
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
		       base_servings, base_cooked_portions,
		       is_leftover, leftover_source_meal_id, locked, ai_run_id, status
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

// ListMealsByHouseholdRange returns every meal scheduled between from and to
// (inclusive, YYYY-MM-DD) across all of the household's plans. Used by the
// dashboard calendar, which spans weeks/months rather than a single plan.
func (s *store) ListMealsByHouseholdRange(ctx context.Context, householdID int64, from, to string) ([]*Meal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.plan_id, m.day, m.slot, m.title, m.effort, m.servings,
		       m.cooked_portions, m.base_servings, m.base_cooked_portions,
		       m.is_leftover, m.leftover_source_meal_id,
		       m.locked, m.ai_run_id, m.status
		FROM meals m
		JOIN plans p ON p.id = m.plan_id
		WHERE p.household_id = ? AND m.day >= ? AND m.day <= ?
		ORDER BY m.day, CASE m.slot WHEN 'breakfast' THEN 0 WHEN 'lunch' THEN 1 ELSE 2 END`,
		householdID, from, to)
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

func (s *store) UpdateMealTitle(ctx context.Context, mealID int64, title, effort string, servings, cookedPortions int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE meals SET title = ?, effort = ?, servings = ?, cooked_portions = ? WHERE id = ?`,
		title, effort, servings, cookedPortions, mealID,
	)
	return err
}

func (s *store) DeleteMealIngredients(ctx context.Context, mealID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meal_ingredients WHERE meal_id = ?`, mealID)
	return err
}

func (s *store) getMealByID(ctx context.Context, mealID int64) (*Meal, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, plan_id, day, slot, title, effort, servings, cooked_portions,
		       base_servings, base_cooked_portions,
		       is_leftover, leftover_source_meal_id, locked, ai_run_id, status
		FROM meals WHERE id = ?`, mealID)
	return scanMealRow(row)
}

// SetMealStatus sets one meal's own cooking status - "skip this meal" /
// "eating out" from the meal-card icons, independent of any other meal that
// day. Validated against the same vocabulary as the day-level status
// (ValidDayStatus, db/models.go) since the two share it.
func (s *store) SetMealStatus(ctx context.Context, mealID int64, status string) error {
	if !ValidDayStatus(status) {
		return fmt.Errorf("unknown meal status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE meals SET status = ? WHERE id = ?`, status, mealID)
	return err
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
		&m.BaseServings, &m.BaseCookedPortions,
		&isLeftover, &m.LeftoverSourceMealID,
		&locked, &m.AIRunID, &m.Status,
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
		&m.BaseServings, &m.BaseCookedPortions,
		&isLeftover, &m.LeftoverSourceMealID,
		&locked, &m.AIRunID, &m.Status,
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

// DeleteMeal removes one meal and everything hanging off it.
//
// meal_recipes and meal_ingredients cascade (00004_plans.sql), but
// leftover_source_meal_id does not: it is a plain reference, so deleting a
// meal that some later day was eating leftovers from would leave that day
// pointing at a row that no longer exists. Those references are cleared first,
// turning any such meal back into an ordinary one rather than a dangling
// leftover.
//
// Both steps run in one transaction so a failure cannot leave half of it done.
func (s *store) DeleteMeal(ctx context.Context, mealID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE meals SET is_leftover = 0, leftover_source_meal_id = NULL
		WHERE leftover_source_meal_id = ?`, mealID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meals WHERE id = ?`, mealID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMealBaseline records a meal's current amounts as its as-created baseline
// (00017): base_servings, base_cooked_portions, and every ingredient's
// base_quantity.
//
// ScaleMealsForDay always rescales from these, never from the current values,
// which is what makes repeated headcount changes idempotent. A meal created
// outside plan generation - materialised from a saved recipe - is written at
// whatever the day needed at the time, so that amount is its baseline; without
// this call its base columns stay 0 and the first rescale would find nothing
// to scale from and skip the meal entirely.
func (s *store) SetMealBaseline(ctx context.Context, mealID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE meals
		SET base_servings = servings, base_cooked_portions = cooked_portions
		WHERE id = ?`, mealID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE meal_ingredients SET base_quantity = quantity WHERE meal_id = ?`, mealID); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveMeal relocates a meal to another day and/or slot within its own plan.
//
// Whatever already occupies the destination is swapped into the mover's old
// place rather than being overwritten. A move that silently deleted the meal
// it landed on would be the single most destructive thing the assistant could
// do by misreading a day name, and a swap is almost always what was meant
// anyway ("move taco night to Friday" wants Friday's meal to go somewhere).
//
// Returns the displaced meal's title, or "" when the destination was empty.
func (s *store) MoveMeal(ctx context.Context, mealID int64, day, slot string) (displaced string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var planID int64
	var fromDay, fromSlot string
	if err := tx.QueryRowContext(ctx,
		`SELECT plan_id, day, slot FROM meals WHERE id = ?`, mealID).
		Scan(&planID, &fromDay, &fromSlot); err != nil {
		return "", err
	}
	if fromDay == day && fromSlot == slot {
		return "", nil
	}

	var otherID int64
	var otherTitle string
	switch err := tx.QueryRowContext(ctx,
		`SELECT id, title FROM meals WHERE plan_id = ? AND day = ? AND slot = ?`,
		planID, day, slot).Scan(&otherID, &otherTitle); {
	case err == nil:
		displaced = otherTitle
	case errors.Is(err, sql.ErrNoRows):
		// Destination is empty; nothing to displace.
	default:
		return "", err
	}

	// A swap needs three writes, not two. UNIQUE(plan_id, day, slot) is checked
	// per row as each UPDATE runs, so moving the displaced meal into the
	// mover's place first collides with the mover, which is still sitting
	// there. Parking it on a sentinel day frees both squares before either
	// real move happens. `day` is unconstrained TEXT (00004_plans.sql), and the
	// sentinel never escapes this transaction.
	if otherID != 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE meals SET day = ? WHERE id = ?`, "moving-"+fromDay, otherID); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meals SET day = ?, slot = ? WHERE id = ?`, day, slot, mealID); err != nil {
		return "", err
	}
	if otherID != 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE meals SET day = ?, slot = ? WHERE id = ?`, fromDay, fromSlot, otherID); err != nil {
			return "", err
		}
	}
	return displaced, tx.Commit()
}
