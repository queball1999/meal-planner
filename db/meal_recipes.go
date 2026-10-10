package db

import (
	"context"
	"database/sql"
	"errors"
)

func (s *store) CreateMealRecipe(ctx context.Context, p CreateMealRecipeParams) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO meal_recipes (meal_id, steps_json, servings, notes)
		VALUES (?, ?, ?, ?)`,
		p.MealID, p.StepsJSON, p.Servings, p.Notes,
	)
	return err
}

// SetMealSteps replaces a meal's cooking steps and the yield they are written
// for, creating its meal_recipes row if it has none. Notes on an existing row
// are the user's own and are left alone.
func (s *store) SetMealSteps(ctx context.Context, mealID int64, stepsJSON string, servings int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO meal_recipes (meal_id, steps_json, servings)
		VALUES (?, ?, ?)
		ON CONFLICT(meal_id) DO UPDATE SET
		  steps_json = excluded.steps_json,
		  servings   = excluded.servings`,
		mealID, stepsJSON, servings,
	)
	return err
}

func (s *store) GetMealRecipe(ctx context.Context, mealID int64) (*MealRecipe, error) {
	var r MealRecipe
	err := s.db.QueryRowContext(ctx, `
		SELECT id, meal_id, steps_json, servings, notes
		FROM meal_recipes WHERE meal_id = ?`, mealID).
		Scan(&r.ID, &r.MealID, &r.StepsJSON, &r.Servings, &r.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
