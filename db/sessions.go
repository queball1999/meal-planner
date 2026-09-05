package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *store) CreateSession(ctx context.Context, userID int64, tokenHash, ipAddr, userAgent string, expiresAt time.Time) (*Session, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at, ip_address, user_agent)
		 VALUES (?, ?, ?, ?, ?)`,
		userID, tokenHash, expiresAt.UTC().Format(time.RFC3339), ipAddr, userAgent)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create session last id: %w", err)
	}
	_ = id // LastInsertId not used; re-query by hash for the full row
	return s.GetSessionByTokenHash(ctx, tokenHash)
}

func (s *store) GetSessionByTokenHash(ctx context.Context, hash string) (*Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, token_hash, expires_at, ip_address, user_agent, created_at
		   FROM sessions WHERE token_hash = ?`,
		hash)
	var sess Session
	var expiresAt, createdAt string
	err := row.Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &expiresAt, &sess.IPAddress, &sess.UserAgent, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &sess, nil
}

func (s *store) DeleteSession(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}
