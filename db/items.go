package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const itemColumns = `id, household_id, name, normalized_term, category, stock_unit,
	default_purchase_qty, image_path, image_attribution, image_source_url,
	source, notes, created_at, updated_at`

func (s *store) CreateItem(ctx context.Context, p CreateItemParams) (*Item, error) {
	if p.Source == "" {
		p.Source = "auto"
	}
	if p.StockUnit == "" {
		p.StockUnit = "each"
	}
	if p.DefaultPurchaseQty == 0 {
		p.DefaultPurchaseQty = 1
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO items
			(household_id, name, normalized_term, category, stock_unit,
			 default_purchase_qty, image_source_url, image_attribution, source, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.HouseholdID, p.Name, p.NormalizedTerm, p.Category, p.StockUnit,
		p.DefaultPurchaseQty, p.ImageSourceURL, p.ImageAttribution, p.Source, p.Notes,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetItem(ctx, id)
}

func (s *store) GetItem(ctx context.Context, id int64) (*Item, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE id = ?`, id)
	return scanItem(row)
}

// GetItemByTerm returns the household's catalog item for a normalized term, or
// nil, nil when none exists yet.
func (s *store) GetItemByTerm(ctx context.Context, householdID int64, term string) (*Item, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE household_id = ? AND normalized_term = ?`,
		householdID, term)
	return scanItem(row)
}

func (s *store) ListItems(ctx context.Context, householdID int64) ([]*Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE household_id = ? ORDER BY name COLLATE NOCASE`,
		householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItemRows(rows)
}

// FilterItems returns catalog items matching an optional name substring and/or
// exact category. Empty filter fields are ignored.
func (s *store) FilterItems(ctx context.Context, householdID int64, f ItemFilter) ([]*Item, error) {
	q := `SELECT ` + itemColumns + ` FROM items WHERE household_id = ?`
	args := []any{householdID}
	if t := strings.TrimSpace(f.Q); t != "" {
		q += ` AND name LIKE ?`
		args = append(args, "%"+t+"%")
	}
	if c := strings.TrimSpace(f.Category); c != "" {
		q += ` AND category = ?`
		args = append(args, c)
	}
	q += ` ORDER BY name COLLATE NOCASE`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItemRows(rows)
}

// UpdateItem rewrites the editable fields of a catalog item. ImagePath is only
// written when non-empty; use ClearItemImage to drop one.
func (s *store) UpdateItem(ctx context.Context, p UpdateItemParams) error {
	if p.ImagePath != "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE items
			SET name = ?, category = ?, stock_unit = ?, default_purchase_qty = ?,
			    notes = ?, image_path = ?,
			    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE id = ?`,
			p.Name, p.Category, p.StockUnit, p.DefaultPurchaseQty, p.Notes, p.ImagePath, p.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE items
		SET name = ?, category = ?, stock_unit = ?, default_purchase_qty = ?, notes = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`,
		p.Name, p.Category, p.StockUnit, p.DefaultPurchaseQty, p.Notes, p.ID)
	return err
}

func (s *store) DeleteItem(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	return err
}

// SetItemImage records a freshly stored image path plus its required credit line.
func (s *store) SetItemImage(ctx context.Context, id int64, imagePath, attribution string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE items
		SET image_path = ?, image_attribution = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, imagePath, attribution, id)
	return err
}

// ClearItemImage drops the stored image reference (keeps image_source_url so a
// later view can lazily re-fetch).
func (s *store) ClearItemImage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE items SET image_path = '' WHERE id = ?`, id)
	return err
}

// SetItemImageSource records a freefoodphotos match found for an item that
// had none (see the background image backfill scheduler) - image_path is
// left untouched, so the next lazy-fetch or backfill tick downloads it.
func (s *store) SetItemImageSource(ctx context.Context, id int64, sourceURL, attribution string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE items
		SET image_source_url = ?, image_attribution = ?,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, sourceURL, attribution, id)
	return err
}

// ── scan helpers ─────────────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItemInto(sc rowScanner) (*Item, error) {
	var it Item
	var createdAt, updatedAt string
	err := sc.Scan(
		&it.ID, &it.HouseholdID, &it.Name, &it.NormalizedTerm, &it.Category, &it.StockUnit,
		&it.DefaultPurchaseQty, &it.ImagePath, &it.ImageAttribution, &it.ImageSourceURL,
		&it.Source, &it.Notes, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	it.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	it.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &it, nil
}

func scanItem(row *sql.Row) (*Item, error) {
	it, err := scanItemInto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

func scanItemRows(rows *sql.Rows) ([]*Item, error) {
	var out []*Item
	for rows.Next() {
		it, err := scanItemInto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
