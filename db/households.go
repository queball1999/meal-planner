package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *store) CreateHousehold(ctx context.Context, p CreateHouseholdParams) (*Household, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO households
		     (name, weekly_budget_cents, country, zip_code, timezone, household_size)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.WeeklyBudgetCents, p.Country, p.ZIPCode, p.Timezone, p.HouseholdSize)
	if err != nil {
		return nil, fmt.Errorf("create household: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create household last id: %w", err)
	}
	return s.getHouseholdByID(ctx, id)
}

// GetHousehold returns household id, or nil, nil if there is no such row.
func (s *store) GetHousehold(ctx context.Context, id int64) (*Household, error) {
	return s.getHouseholdByID(ctx, id)
}

func (s *store) getHouseholdByID(ctx context.Context, id int64) (*Household, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+householdCols+` FROM households WHERE id = ?`, id)
	return scanHousehold(row)
}

const householdCols = `id, name, weekly_budget_cents, country, zip_code, region_label,
		        timezone, household_size, created_at`

// ListHouseholds returns every household on the instance, oldest first. For
// background jobs and the admin view - request handlers use the caller's
// memberships instead.
func (s *store) ListHouseholds(ctx context.Context) ([]*Household, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+householdCols+` FROM households ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list households: %w", err)
	}
	defer rows.Close()
	var out []*Household
	for rows.Next() {
		h, err := scanHouseholdRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RenameHousehold changes a household's display name.
func (s *store) RenameHousehold(ctx context.Context, id int64, name string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE households SET name = ? WHERE id = ?`, name, id); err != nil {
		return fmt.Errorf("rename household: %w", err)
	}
	return nil
}

// DeleteHousehold removes a household and, through ON DELETE CASCADE, every
// plan, pantry row, item, recipe, store and membership that belongs to it.
func (s *store) DeleteHousehold(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM households WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete household: %w", err)
	}
	return nil
}

func scanHousehold(row *sql.Row) (*Household, error) {
	h, err := scanHouseholdRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

func scanHouseholdRow(row interface{ Scan(...any) error }) (*Household, error) {
	var h Household
	var createdAt string
	err := row.Scan(
		&h.ID, &h.Name, &h.WeeklyBudgetCents,
		&h.Country, &h.ZIPCode, &h.RegionLabel,
		&h.Timezone, &h.HouseholdSize, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("scan household: %w", err)
	}
	h.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &h, nil
}
