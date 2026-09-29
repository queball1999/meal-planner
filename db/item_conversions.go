package db

import "context"

// UpsertUnitConversion inserts or replaces one conversion edge. A nil ItemID
// writes a global conversion; the UNIQUE(item_id, from_unit, to_unit) index
// makes the upsert idempotent. SQLite treats NULL as distinct in UNIQUE
// indexes, so global rows are de-duped explicitly before insert.
func (s *store) UpsertUnitConversion(ctx context.Context, p UpsertUnitConversionParams) error {
	if p.ItemID == nil {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM unit_conversions WHERE item_id IS NULL AND from_unit = ? AND to_unit = ?`,
			p.FromUnit, p.ToUnit); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO unit_conversions (item_id, from_unit, to_unit, factor) VALUES (NULL, ?, ?, ?)`,
			p.FromUnit, p.ToUnit, p.Factor)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO unit_conversions (item_id, from_unit, to_unit, factor)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(item_id, from_unit, to_unit) DO UPDATE SET factor = excluded.factor`,
		*p.ItemID, p.FromUnit, p.ToUnit, p.Factor)
	return err
}

// ListGlobalConversions returns every conversion edge with no item_id.
func (s *store) ListGlobalConversions(ctx context.Context) ([]*UnitConversion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, item_id, from_unit, to_unit, factor, derived FROM unit_conversions WHERE item_id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConversionRows(rows)
}

// ListConversionsForItem returns the item's own conversion edges plus every
// global edge, so a caller has the full graph for that item in one slice.
func (s *store) ListConversionsForItem(ctx context.Context, itemID int64) ([]*UnitConversion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, from_unit, to_unit, factor, derived
		FROM unit_conversions
		WHERE item_id = ? OR item_id IS NULL`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConversionRows(rows)
}

// DeleteUnitConversion deletes one of itemID's conversions. The item_id
// match is the ownership check (QSS security design §17): a conversion id
// belonging to another item - or a global one, item_id NULL - deletes
// nothing and reports ErrNotFound.
func (s *store) DeleteUnitConversion(ctx context.Context, itemID, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM unit_conversions WHERE id = ? AND item_id = ?`, id, itemID)
	if err != nil {
		return err
	}
	return oneRow(res)
}

// ReplaceDerivedItemConversions swaps an item's machine-generated (derived)
// conversion rows for a fresh set in one transaction. Hand-entered / seeded rows
// (derived = 0) are left untouched, and a derived edge that collides with an
// existing (from_unit, to_unit) pair is dropped so the manual value always wins.
func (s *store) ReplaceDerivedItemConversions(ctx context.Context, itemID int64, edges []UpsertUnitConversionParams) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM unit_conversions WHERE item_id = ? AND derived = 1`, itemID); err != nil {
		return err
	}
	for _, e := range edges {
		if e.Factor <= 0 || e.FromUnit == "" || e.ToUnit == "" || e.FromUnit == e.ToUnit {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO unit_conversions (item_id, from_unit, to_unit, factor, derived)
			VALUES (?, ?, ?, ?, 1)
			ON CONFLICT(item_id, from_unit, to_unit) DO NOTHING`,
			itemID, e.FromUnit, e.ToUnit, e.Factor); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanConversionRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*UnitConversion, error) {
	var out []*UnitConversion
	for rows.Next() {
		var c UnitConversion
		var itemID *int64
		if err := rows.Scan(&c.ID, &itemID, &c.FromUnit, &c.ToUnit, &c.Factor, &c.Derived); err != nil {
			return nil, err
		}
		c.ItemID = itemID
		out = append(out, &c)
	}
	return out, rows.Err()
}
