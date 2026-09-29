package db

import (
	"context"
	"fmt"
	"time"
)

// attemptTimeLayout is fixed-width (no trimmed zeros, unlike RFC3339Nano) so
// attempted_at values sort and compare correctly as text.
const attemptTimeLayout = "2006-01-02T15:04:05.000000000Z"

// RecordLoginAttempt stores one sign-in attempt (00034_login_attempts.sql).
// Rows older than a day are pruned on the way; the lockout never looks back
// that far.
func (s *store) RecordLoginAttempt(ctx context.Context, username, clientIP string, success bool, at time.Time) error {
	ok := 0
	if success {
		ok = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO login_attempts (username, client_ip, attempted_at, success) VALUES (?, ?, ?, ?)`,
		username, clientIP, at.UTC().Format(attemptTimeLayout), ok); err != nil {
		return fmt.Errorf("record login attempt: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE attempted_at < ?`,
		at.Add(-24*time.Hour).UTC().Format(attemptTimeLayout))
	return nil
}

// FailedLoginTimes returns the times of failed attempts since since, newest
// first. An empty username or clientIP doesn't filter on that column.
func (s *store) FailedLoginTimes(ctx context.Context, username, clientIP string, since time.Time) ([]time.Time, error) {
	q := `SELECT attempted_at FROM login_attempts WHERE success = 0 AND attempted_at >= ?`
	args := []any{since.UTC().Format(attemptTimeLayout)}
	if username != "" {
		q += ` AND username = ?`
		args = append(args, username)
	}
	if clientIP != "" {
		q += ` AND client_ip = ?`
		args = append(args, clientIP)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY attempted_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed login times: %w", err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan login attempt: %w", err)
		}
		t, err := time.Parse(attemptTimeLayout, raw)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClearLoginFailures forgets the failed attempts for one (username, IP)
// pair - a successful sign-in from there.
func (s *store) ClearLoginFailures(ctx context.Context, username, clientIP string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM login_attempts WHERE username = ? AND client_ip = ? AND success = 0`, username, clientIP)
	return err
}
