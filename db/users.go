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

// CountUsers is the first-run test: zero accounts means the setup wizard is
// open. Deliberately not "zero households" - an admin deleting the last
// household must not reopen setup to whoever reaches it first.
func (s *store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// ListUsers returns every account on the instance, by username.
func (s *store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, role, totp_enabled, created_at
		   FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserRole changes a user's instance role. Demoting the last admin is
// refused: nobody could then reach Settings or create accounts.
func (s *store) SetUserRole(ctx context.Context, userID int64, role string) error {
	if role != InstanceRoleAdmin && role != InstanceRoleMember {
		return fmt.Errorf("set user role: invalid role %q", role)
	}
	if role != InstanceRoleAdmin {
		if err := s.ensureAnotherAdmin(ctx, userID); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, userID); err != nil {
		return fmt.Errorf("set user role: %w", err)
	}
	return nil
}

// DeleteUser removes an account; sessions and memberships cascade. The last
// admin can't be deleted, nor can the last owner of any household.
func (s *store) DeleteUser(ctx context.Context, userID int64) error {
	if err := s.ensureAnotherAdmin(ctx, userID); err != nil {
		return err
	}
	var orphaned int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM household_memberships m
		  WHERE m.user_id = ? AND m.role = 'owner'
		    AND NOT EXISTS (SELECT 1 FROM household_memberships o
		                     WHERE o.household_id = m.household_id
		                       AND o.role = 'owner' AND o.user_id <> m.user_id)`,
		userID).Scan(&orphaned); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if orphaned > 0 {
		return ErrLastOwner
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// ErrLastAdmin is returned when a change would leave the instance with no admin.
var ErrLastAdmin = errors.New("the instance needs at least one admin")

// ensureAnotherAdmin passes when userID isn't an admin, or another admin exists.
func (s *store) ensureAnotherAdmin(ctx context.Context, userID int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND id <> ?`, userID).Scan(&n); err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	if n > 0 {
		return nil
	}
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("user role: %w", err)
	}
	if role == InstanceRoleAdmin {
		return ErrLastAdmin
	}
	return nil
}

func scanUser(row *sql.Row) (*User, error) {
	u, err := scanUserRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func scanUserRow(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var createdAt string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPEnabled, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &u, nil
}
