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
