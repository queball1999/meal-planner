package db

import (
	"context"
	"database/sql"
	"errors"
)

func (s *store) CreateShoppingListItem(ctx context.Context, p CreateShoppingListItemParams) (*ShoppingListItem, error) {
	var storeID interface{}
	if p.StoreID != nil {
		storeID = *p.StoreID
	}
	var itemID interface{}
	if p.ItemID != nil {
		itemID = *p.ItemID
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO shopping_list_items
			(plan_id, store_id, item_id, meal_ingredient_refs, display_name,
			 buy_quantity, pack_size, purchase_unit,
			 unit_price_cents, line_total_cents, price_source, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PlanID, storeID, itemID, p.MealIngredientRefs, p.DisplayName,
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
		ItemID:             p.ItemID,
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
		SELECT id, plan_id, store_id, item_id, meal_ingredient_refs, display_name,
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
		var storeID, itemID *int64
		var checked, inPantry int
		if err := rows.Scan(
			&item.ID, &item.PlanID, &storeID, &itemID, &item.MealIngredientRefs, &item.DisplayName,
			&item.BuyQuantity, &item.PackSize, &item.PurchaseUnit,
			&item.UnitPriceCents, &item.LineTotalCents, &item.PriceSource, &item.Confidence,
			&checked, &inPantry,
		); err != nil {
			return nil, err
		}
		item.StoreID = storeID
		item.ItemID = itemID
		item.Checked = checked != 0
		item.InPantry = inPantry != 0
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (s *store) GetShoppingListItem(ctx context.Context, id int64) (*ShoppingListItem, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, plan_id, store_id, item_id, meal_ingredient_refs, display_name,
		       buy_quantity, pack_size, purchase_unit,
		       unit_price_cents, line_total_cents, price_source, confidence, checked, in_pantry
		FROM shopping_list_items
		WHERE id = ?`, id)

	var item ShoppingListItem
	var storeID, itemID *int64
	var checked, inPantry int
	err := row.Scan(
		&item.ID, &item.PlanID, &storeID, &itemID, &item.MealIngredientRefs, &item.DisplayName,
		&item.BuyQuantity, &item.PackSize, &item.PurchaseUnit,
		&item.UnitPriceCents, &item.LineTotalCents, &item.PriceSource, &item.Confidence,
		&checked, &inPantry,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.StoreID = storeID
	item.ItemID = itemID
	item.Checked = checked != 0
	item.InPantry = inPantry != 0
	return &item, nil
}

// UpdateShoppingListItemPrice rewrites one line's resolved store/item, buy
// quantity, price, and confidence after a manual price edit.
func (s *store) UpdateShoppingListItemPrice(ctx context.Context, p UpdateShoppingListItemPriceParams) error {
	var storeID, itemID interface{}
	if p.StoreID != nil {
		storeID = *p.StoreID
	}
	if p.ItemID != nil {
		itemID = *p.ItemID
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE shopping_list_items
		SET store_id = ?, item_id = ?, buy_quantity = ?, pack_size = ?, purchase_unit = ?,
		    unit_price_cents = ?, line_total_cents = ?, price_source = ?, confidence = ?
		WHERE id = ?`,
		storeID, itemID, p.BuyQuantity, p.PackSize, p.PurchaseUnit,
		p.UnitPriceCents, p.LineTotalCents, p.PriceSource, p.Confidence, p.ID)
	return err
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

// DeleteAllShoppingListItemsForHousehold clears every generated shopping-list
// line across every plan for a household (Settings → Danger zone), plus the
// Home Assistant sync mapping (self-heals: re-added on the next push) and
// each affected plan's cached total, which would otherwise still show a
// price for a now-empty list. Plans and meals themselves are untouched.
func (s *store) DeleteAllShoppingListItemsForHousehold(ctx context.Context, householdID int64) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM shopping_list_items
		WHERE plan_id IN (SELECT id FROM plans WHERE household_id = ?)`, householdID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE plans SET total_cents = 0, confidence_summary = '' WHERE household_id = ?`, householdID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM ha_sync_map WHERE household_id = ?`, householdID)
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

// SetShoppingListItemItem repoints one shopping-list line at a catalog item.
// Used when a person confirms what an unmatched line actually means.
func (s *store) SetShoppingListItemItem(ctx context.Context, id int64, itemID *int64) error {
	var iid any
	if itemID != nil {
		iid = *itemID
	}
	_, err := s.db.ExecContext(ctx, `UPDATE shopping_list_items SET item_id = ? WHERE id = ?`, iid, id)
	return err
}
