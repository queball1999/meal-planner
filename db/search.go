package db

import (
	"context"
	"fmt"
	"strings"
)

// SearchCatalogRecipes matches title or tags. Leftovers-titled recipes are
// never returned: every caller (global search, the recipe picker, the chat
// agent, the plan generator's search_recipes tool) wants recipes to cook.
func (s *store) SearchCatalogRecipes(ctx context.Context, householdID int64, q string) ([]*CatalogRecipe, error) {
	like := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, title, source_kind, source_url, source_site, source_author,
		       image_path, servings, prep_minutes, cook_minutes, tags, created_at
		FROM catalog_recipes
		WHERE household_id = ?
		  AND (lower(title) LIKE lower(?) OR lower(tags) LIKE lower(?))
		  AND lower(title) NOT LIKE '%leftover%' -- IsLeftoverTitle, before the LIMIT
		ORDER BY title COLLATE NOCASE
		LIMIT 20`,
		householdID, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CatalogRecipe
	for rows.Next() {
		r, err := scanCatalogRecipeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) SearchPantryItems(ctx context.Context, householdID int64, q string) ([]*PantryItem, error) {
	like := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at, item_id
		FROM pantry_items
		WHERE household_id = ?
		  AND (lower(name) LIKE lower(?) OR lower(normalized_term) LIKE lower(?))
		ORDER BY name COLLATE NOCASE
		LIMIT 20`,
		householdID, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PantryItem
	for rows.Next() {
		item, err := scanPantryItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *store) SearchMealTitles(ctx context.Context, householdID int64, q string) ([]*Meal, error) {
	like := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.plan_id, m.day, m.slot, m.title, m.effort,
		       m.servings, m.cooked_portions, m.is_leftover,
		       m.leftover_source_meal_id, m.locked, m.ai_run_id
		FROM meals m
		JOIN plans p ON p.id = m.plan_id
		WHERE p.household_id = ?
		  AND lower(m.title) LIKE lower(?)
		ORDER BY m.day DESC, m.slot
		LIMIT 20`,
		householdID, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Meal
	for rows.Next() {
		var m Meal
		if err := rows.Scan(
			&m.ID, &m.PlanID, &m.Day, &m.Slot, &m.Title, &m.Effort,
			&m.Servings, &m.CookedPortions, &m.IsLeftover,
			&m.LeftoverSourceMealID, &m.Locked, &m.AIRunID,
		); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *store) FilterCatalogRecipes(ctx context.Context, householdID int64, f CatalogRecipeFilter) ([]*CatalogRecipe, error) {
	query := `
		SELECT id, household_id, title, source_kind, source_url, source_site, source_author,
		       image_path, servings, prep_minutes, cook_minutes, tags, created_at
		FROM catalog_recipes
		WHERE household_id = ?`
	args := []any{householdID}

	if f.Q != "" {
		query += ` AND (lower(title) LIKE lower(?) OR lower(tags) LIKE lower(?))`
		like := "%" + f.Q + "%"
		args = append(args, like, like)
	}
	if f.Tag != "" {
		query += ` AND lower(tags) LIKE lower(?)`
		args = append(args, fmt.Sprintf(`%%%s%%`, f.Tag))
	}
	if f.Source != "" {
		query += ` AND source_kind = ?`
		args = append(args, f.Source)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CatalogRecipe
	for rows.Next() {
		r, err := scanCatalogRecipeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) FilterPantryItems(ctx context.Context, householdID int64, q string) ([]*PantryItem, error) {
	if strings.TrimSpace(q) == "" {
		return s.ListPantryItems(ctx, householdID)
	}
	like := "%" + q + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at, item_id
		FROM pantry_items
		WHERE household_id = ?
		  AND (lower(name) LIKE lower(?) OR lower(normalized_term) LIKE lower(?))
		ORDER BY name COLLATE NOCASE`,
		householdID, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PantryItem
	for rows.Next() {
		item, err := scanPantryItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *store) GetPantryItemByBarcode(ctx context.Context, householdID int64, code string) (*PantryItem, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at, item_id
		FROM pantry_items WHERE household_id = ? AND barcode = ?`,
		householdID, code)
	return scanPantryItem(row)
}

func (s *store) IncrementPantryItem(ctx context.Context, id int64, delta float64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE pantry_items SET quantity_on_hand = quantity_on_hand + ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, delta, id)
	return err
}

// GetItemProductMapByBarcode finds a product pinned to one of householdID's
// stores - scoped through stores so a scan can't surface another household's
// pinned products.
func (s *store) GetItemProductMapByBarcode(ctx context.Context, householdID int64, code string) (*ItemProductMap, error) {
	var m ItemProductMap
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.store_id, m.normalized_term, m.chosen_product, m.pack_size, m.purchase_unit, m.barcode, m.updated_at
		  FROM item_product_map m JOIN stores st ON st.id = m.store_id
		 WHERE m.barcode = ? AND st.household_id = ? LIMIT 1`, code, householdID,
	).Scan(&m.ID, &m.StoreID, &m.NormalizedTerm, &m.ChosenProduct,
		&m.PackSize, &m.PurchaseUnit, &m.Barcode, &updatedAt)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	return &m, nil
}
