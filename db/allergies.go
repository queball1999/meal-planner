package db

import (
	"context"
	"fmt"
)

func (s *store) SetAllergies(ctx context.Context, householdID int64, terms []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set allergies begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM allergies WHERE household_id = ?`, householdID); err != nil {
		return fmt.Errorf("set allergies delete: %w", err)
	}

	for _, term := range terms {
		if term == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO allergies (household_id, term) VALUES (?, ?)`,
			householdID, term); err != nil {
			return fmt.Errorf("set allergies insert %q: %w", term, err)
		}
	}
	return tx.Commit()
}

func (s *store) ListAllergies(ctx context.Context, householdID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT term FROM allergies WHERE household_id = ? ORDER BY term`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list allergies: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
