package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *store) CreatePantryItem(ctx context.Context, p CreatePantryItemParams) (*PantryItem, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pantry_items
		  (household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(household_id, normalized_term) DO UPDATE SET
		  name            = excluded.name,
		  quantity_on_hand = pantry_items.quantity_on_hand + excluded.quantity_on_hand,
		  unit            = excluded.unit,
		  barcode         = CASE WHEN excluded.barcode != '' THEN excluded.barcode ELSE pantry_items.barcode END,
		  updated_at      = excluded.updated_at`,
		p.HouseholdID, p.Name, p.NormalizedTerm, p.QuantityOnHand,
		p.Unit, p.Barcode, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	return s.getPantryItemByTerm(ctx, p.HouseholdID, p.NormalizedTerm)
}

func (s *store) ListPantryItems(ctx context.Context, householdID int64) ([]*PantryItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at
		FROM pantry_items WHERE household_id = ?
		ORDER BY name COLLATE NOCASE`, householdID)
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

func (s *store) UpdatePantryItem(ctx context.Context, p UpdatePantryItemParams) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE pantry_items
		SET quantity_on_hand = ?, unit = ?, updated_at = ?
		WHERE id = ?`,
		p.QuantityOnHand, p.Unit, time.Now().UTC().Format(time.RFC3339), p.ID,
	)
	return err
}

func (s *store) DeletePantryItem(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pantry_items WHERE id = ?`, id)
	return err
}

func (s *store) getPantryItemByTerm(ctx context.Context, householdID int64, term string) (*PantryItem, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at
		FROM pantry_items WHERE household_id = ? AND normalized_term = ?`,
		householdID, term)
	item, err := scanPantryItemRow(row)
	if err != nil {
		return nil, err
	}
	return item, nil
}

type pantryScanner interface {
	Scan(dest ...any) error
}

func scanPantryItem(sc pantryScanner) (*PantryItem, error) {
	var item PantryItem
	var updatedAt string
	err := sc.Scan(
		&item.ID, &item.HouseholdID, &item.Name, &item.NormalizedTerm,
		&item.QuantityOnHand, &item.Unit, &item.Barcode, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &item, nil
}

func scanPantryItemRow(row *sql.Row) (*PantryItem, error) {
	var item PantryItem
	var updatedAt string
	err := row.Scan(
		&item.ID, &item.HouseholdID, &item.Name, &item.NormalizedTerm,
		&item.QuantityOnHand, &item.Unit, &item.Barcode, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &item, nil
}
