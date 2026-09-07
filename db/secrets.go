package db

import (
	"context"
	"database/sql"
	"errors"
)

// SetSecret stores (or replaces) an encrypted credential. ciphertext is
// produced by cryptbox.Seal; an empty string clears the row's value.
func (s *store) SetSecret(ctx context.Context, key, ciphertext string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO secrets (key, ciphertext, updated_at)
		VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(key) DO UPDATE SET
			ciphertext = excluded.ciphertext,
			updated_at = excluded.updated_at`,
		key, ciphertext)
	return err
}

// GetSecret returns the stored ciphertext for key and whether a row exists.
func (s *store) GetSecret(ctx context.Context, key string) (string, bool, error) {
	var ct string
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext FROM secrets WHERE key = ?`, key).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ct, true, nil
}
