package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SeedSetting records key's current .env value with source 'env'.
//
// An env-sourced row is only ever a mirror of .env, so it is refreshed on
// every boot - otherwise editing .env would leave the Settings page showing a
// stale value the app is not using. An admin-edited row ('admin') always wins
// and is never touched here.
func (s *store) SeedSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, source, updated_at)
		 VALUES (?, ?, 'env', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		 ON CONFLICT(key) DO UPDATE SET
		     value      = excluded.value,
		     updated_at = excluded.updated_at
		 WHERE settings.source = 'env'`,
		key, value)
	if err != nil {
		return fmt.Errorf("seed setting %s: %w", key, err)
	}
	return nil
}

// SetSetting records an admin edit, marking the row's source as 'admin' so a
// future SeedSetting call never clobbers it.
func (s *store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, source, updated_at)
		 VALUES (?, ?, 'admin', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		 ON CONFLICT(key) DO UPDATE SET
		     value      = excluded.value,
		     source     = 'admin',
		     updated_at = excluded.updated_at`,
		key, value)
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// ListSettings returns every stored setting.
func (s *store) ListSettings(ctx context.Context) ([]*Setting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, source, updated_at FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()

	var out []*Setting
	for rows.Next() {
		var st Setting
		var updatedAt string
		if err := rows.Scan(&st.Key, &st.Value, &st.Source, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		st.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &st)
	}
	return out, rows.Err()
}

// GetSetting returns nil, nil when key has never been seeded or set.
func (s *store) GetSetting(ctx context.Context, key string) (*Setting, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT key, value, source, updated_at FROM settings WHERE key = ?`, key)

	var st Setting
	var updatedAt string
	err := row.Scan(&st.Key, &st.Value, &st.Source, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get setting %s: %w", key, err)
	}
	st.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &st, nil
}
