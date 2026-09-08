package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *store) CreateUser(ctx context.Context, username, passwordHash, role string) (*User, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`,
		username, passwordHash, role)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create user last id: %w", err)
	}
	return s.GetUserByID(ctx, id)
}

func (s *store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, totp_enabled, created_at
		   FROM users WHERE username = ? COLLATE NOCASE`,
		username)
	return scanUser(row)
}

func (s *store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, totp_enabled, created_at
		   FROM users WHERE id = ?`,
		id)
	return scanUser(row)
}

// UpdateUserPassword replaces one user's bcrypt hash. The caller is
// responsible for verifying the current password first (see the account page).
func (s *store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update user password: no user with id %d", userID)
	}
	return nil
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var createdAt string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPEnabled, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &u, nil
}
