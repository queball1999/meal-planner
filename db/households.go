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

// GetHousehold returns the single household, or nil, nil if none exists yet.
func (s *store) GetHousehold(ctx context.Context) (*Household, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, weekly_budget_cents, country, zip_code, region_label,
		        timezone, household_size, created_at
		   FROM households LIMIT 1`)
	return scanHousehold(row)
}

func (s *store) getHouseholdByID(ctx context.Context, id int64) (*Household, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, weekly_budget_cents, country, zip_code, region_label,
		        timezone, household_size, created_at
		   FROM households WHERE id = ?`, id)
	return scanHousehold(row)
}

func scanHousehold(row *sql.Row) (*Household, error) {
	var h Household
	var createdAt string
	err := row.Scan(
		&h.ID, &h.Name, &h.WeeklyBudgetCents,
		&h.Country, &h.ZIPCode, &h.RegionLabel,
		&h.Timezone, &h.HouseholdSize, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan household: %w", err)
	}
	h.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &h, nil
}
