package db

import (
	"context"
)

func (s *store) CreateShoppingListItem(ctx context.Context, p CreateShoppingListItemParams) (*ShoppingListItem, error) {
	var storeID interface{}
	if p.StoreID != nil {
		storeID = *p.StoreID
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO shopping_list_items
			(plan_id, store_id, meal_ingredient_refs, display_name,
			 buy_quantity, pack_size, purchase_unit,
			 unit_price_cents, line_total_cents, price_source, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PlanID, storeID, p.MealIngredientRefs, p.DisplayName,
		p.BuyQuantity, p.PackSize, p.PurchaseUnit,
		p.UnitPriceCents, p.LineTotalCents, p.PriceSource, p.Confidence,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	item := &ShoppingListItem{
		ID:                 id,
		PlanID:             p.PlanID,
		StoreID:            p.StoreID,
		MealIngredientRefs: p.MealIngredientRefs,
		DisplayName:        p.DisplayName,
		BuyQuantity:        p.BuyQuantity,
		PackSize:           p.PackSize,
		PurchaseUnit:       p.PurchaseUnit,
		UnitPriceCents:     p.UnitPriceCents,
		LineTotalCents:     p.LineTotalCents,
		PriceSource:        p.PriceSource,
		Confidence:         p.Confidence,
	}
	return item, nil
}

func (s *store) ListShoppingListItems(ctx context.Context, planID int64) ([]*ShoppingListItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, plan_id, store_id, meal_ingredient_refs, display_name,
		       buy_quantity, pack_size, purchase_unit,
		       unit_price_cents, line_total_cents, price_source, confidence, checked, in_pantry
		FROM shopping_list_items
		WHERE plan_id = ?
		ORDER BY store_id, display_name`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ShoppingListItem
	for rows.Next() {
		var item ShoppingListItem
		var storeID *int64
		var checked, inPantry int
		if err := rows.Scan(
			&item.ID, &item.PlanID, &storeID, &item.MealIngredientRefs, &item.DisplayName,
			&item.BuyQuantity, &item.PackSize, &item.PurchaseUnit,
			&item.UnitPriceCents, &item.LineTotalCents, &item.PriceSource, &item.Confidence,
			&checked, &inPantry,
		); err != nil {
			return nil, err
		}
		item.StoreID = storeID
		item.Checked = checked != 0
		item.InPantry = inPantry != 0
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (s *store) CheckShoppingListItem(ctx context.Context, id int64, checked bool) error {
	v := 0
	if checked {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE shopping_list_items SET checked = ? WHERE id = ?`, v, id)
	return err
}

func (s *store) DeleteShoppingListItems(ctx context.Context, planID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shopping_list_items WHERE plan_id = ?`, planID)
	return err
}

func (s *store) MarkShoppingListItemInPantry(ctx context.Context, id int64, inPantry bool) error {
	v := 0
	if inPantry {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE shopping_list_items SET in_pantry = ? WHERE id = ?`, v, id)
	return err
}
