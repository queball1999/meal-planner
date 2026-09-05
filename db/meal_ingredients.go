package db

import (
	"context"
)

func (s *store) CreateMealIngredient(ctx context.Context, p CreateMealIngredientParams) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO meal_ingredients (meal_id, name, quantity, unit, normalized_term)
		VALUES (?, ?, ?, ?, '')`,
		p.MealID, p.Name, p.Quantity, p.Unit,
	)
	return err
}

func (s *store) ListIngredientsByMeal(ctx context.Context, mealID int64) ([]*MealIngredient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, meal_id, name, quantity, unit, normalized_term
		FROM meal_ingredients WHERE meal_id = ?
		ORDER BY id`, mealID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*MealIngredient
	for rows.Next() {
		var ing MealIngredient
		if err := rows.Scan(&ing.ID, &ing.MealID, &ing.Name, &ing.Quantity, &ing.Unit, &ing.NormalizedTerm); err != nil {
			return nil, err
		}
		out = append(out, &ing)
	}
	return out, rows.Err()
}

func (s *store) ListIngredientsByPlan(ctx context.Context, planID int64) ([]*MealIngredient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mi.id, mi.meal_id, mi.name, mi.quantity, mi.unit, mi.normalized_term
		FROM meal_ingredients mi
		JOIN meals m ON m.id = mi.meal_id
		WHERE m.plan_id = ?
		ORDER BY mi.meal_id, mi.id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*MealIngredient
	for rows.Next() {
		var ing MealIngredient
		if err := rows.Scan(&ing.ID, &ing.MealID, &ing.Name, &ing.Quantity, &ing.Unit, &ing.NormalizedTerm); err != nil {
			return nil, err
		}
		out = append(out, &ing)
	}
	return out, rows.Err()
}
