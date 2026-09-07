package db

import (
	"context"
)

func (s *store) CreateMealIngredient(ctx context.Context, p CreateMealIngredientParams) error {
	var itemID interface{}
	if p.ItemID != nil {
		itemID = *p.ItemID
	}
	// base_quantity mirrors quantity at creation: a new ingredient row is
	// unscaled, and every later headcount rescale multiplies the base rather
	// than the current value (00017).
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO meal_ingredients
		  (meal_id, name, quantity, base_quantity, unit, normalized_term, item_id, est_price_cents)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.MealID, p.Name, p.Quantity, p.Quantity, p.Unit, p.NormalizedTerm, itemID, p.EstPriceCents,
	)
	return err
}

// SetMealIngredientItem links an existing ingredient row to a catalog item and
// records its normalized term. Used by the catalog linking pass.
func (s *store) SetMealIngredientItem(ctx context.Context, ingredientID int64, itemID *int64, normalizedTerm string) error {
	var iid interface{}
	if itemID != nil {
		iid = *itemID
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE meal_ingredients SET item_id = ?, normalized_term = ? WHERE id = ?`,
		iid, normalizedTerm, ingredientID)
	return err
}

func (s *store) ListIngredientsByMeal(ctx context.Context, mealID int64) ([]*MealIngredient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, meal_id, name, quantity, base_quantity, unit, normalized_term, item_id, est_price_cents
		FROM meal_ingredients WHERE meal_id = ?
		ORDER BY id`, mealID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMealIngredientRows(rows)
}

// ListIngredientsByPlan returns every ingredient the plan actually needs
// bought - the input to costing and to the shopping list.
//
// Days marked "eating out" or "skipped" are excluded (00019_day_status.sql).
// This is the one place that filter has to live: every path that builds a
// shopping list - CostPlan, EnsureShoppingList, a reprice after a rescale -
// reads its ingredients through here, so a day taken off the plan disappears
// from the list whichever of them runs next.
//
// LEFT JOIN, not JOIN: a plan generated before day rows existed (or one whose
// seed failed) has meals with no matching plan_days row, and an inner join
// would silently drop every ingredient in the plan.
func (s *store) ListIngredientsByPlan(ctx context.Context, planID int64) ([]*MealIngredient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mi.id, mi.meal_id, mi.name, mi.quantity, mi.base_quantity, mi.unit, mi.normalized_term, mi.item_id, mi.est_price_cents
		FROM meal_ingredients mi
		JOIN meals m ON m.id = mi.meal_id
		LEFT JOIN plan_days pd ON pd.plan_id = m.plan_id AND pd.date = m.day
		WHERE m.plan_id = ?
		  AND COALESCE(pd.status, 'cooking') = 'cooking'
		ORDER BY mi.meal_id, mi.id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMealIngredientRows(rows)
}

// ListMealTitlesByIngredientID returns meal titles keyed by meal_ingredient id
// for every ingredient in a plan - used to tag each shopping-list line with
// which meal(s) it was pulled from (§ shopping list "used in" pills).
func (s *store) ListMealTitlesByIngredientID(ctx context.Context, planID int64) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mi.id, m.title
		FROM meal_ingredients mi
		JOIN meals m ON m.id = mi.meal_id
		WHERE m.plan_id = ?`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]string)
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

func scanMealIngredientRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*MealIngredient, error) {
	var out []*MealIngredient
	for rows.Next() {
		var ing MealIngredient
		var itemID *int64
		if err := rows.Scan(&ing.ID, &ing.MealID, &ing.Name, &ing.Quantity, &ing.BaseQuantity, &ing.Unit, &ing.NormalizedTerm, &itemID, &ing.EstPriceCents); err != nil {
			return nil, err
		}
		ing.ItemID = itemID
		out = append(out, &ing)
	}
	return out, rows.Err()
}
