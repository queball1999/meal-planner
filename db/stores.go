package db

import (
	"context"
	"fmt"
	"time"
)

func (s *store) CreateStore(ctx context.Context, p UpsertStoreParams) (*GroceryStore, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO stores (household_id, name, kind) VALUES (?, ?, ?)`,
		p.HouseholdID, p.Name, p.Kind)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create store last id: %w", err)
	}
	return s.getStoreByID(ctx, id)
}

func (s *store) ListStores(ctx context.Context, householdID int64) ([]*GroceryStore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, household_id, name, kind, provider_chain, enabled, created_at
		   FROM stores WHERE household_id = ? ORDER BY name`,
		householdID)
	if err != nil {
		return nil, fmt.Errorf("list stores: %w", err)
	}
	defer rows.Close()

	var out []*GroceryStore
	for rows.Next() {
		gs, err := scanStore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, rows.Err()
}

func (s *store) DeleteStore(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stores WHERE id = ?`, id)
	return err
}

func (s *store) getStoreByID(ctx context.Context, id int64) (*GroceryStore, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, household_id, name, kind, provider_chain, enabled, created_at
		   FROM stores WHERE id = ?`, id)
	return scanStore(row)
}

type storeScanner interface {
	Scan(dest ...any) error
}

func scanStore(sc storeScanner) (*GroceryStore, error) {
	var gs GroceryStore
	var createdAt string
	var enabled int
	if err := sc.Scan(&gs.ID, &gs.HouseholdID, &gs.Name, &gs.Kind,
		&gs.ProviderChain, &enabled, &createdAt); err != nil {
		return nil, fmt.Errorf("scan store: %w", err)
	}
	gs.Enabled = enabled != 0
	gs.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &gs, nil
}
