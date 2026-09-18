package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UpsertScrapeClearance implements Store. One row per store - a fresh solve
// (live or pasted) simply replaces whatever was there before.
func (s *store) UpsertScrapeClearance(ctx context.Context, p ScrapeClearance) error {
	obtainedAt := p.ObtainedAt
	if obtainedAt.IsZero() {
		obtainedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scrape_clearances (store_id, cookies_json, user_agent, obtained_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(store_id) DO UPDATE SET
			cookies_json = excluded.cookies_json,
			user_agent   = excluded.user_agent,
			obtained_at  = excluded.obtained_at,
			expires_at   = excluded.expires_at`,
		p.StoreID, p.CookiesJSON, p.UserAgent,
		obtainedAt.Format(time.RFC3339Nano), p.ExpiresAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetScrapeClearance implements Store. Returns nil, nil when none exists -
// callers should not treat "no clearance saved" as an error.
func (s *store) GetScrapeClearance(ctx context.Context, storeID int64) (*ScrapeClearance, error) {
	var c ScrapeClearance
	var obtainedAt, expiresAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT store_id, cookies_json, user_agent, obtained_at, expires_at
		FROM scrape_clearances WHERE store_id = ?`, storeID).
		Scan(&c.StoreID, &c.CookiesJSON, &c.UserAgent, &obtainedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ObtainedAt, _ = time.Parse(time.RFC3339Nano, obtainedAt)
	c.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	return &c, nil
}

// DeleteScrapeClearance implements Store. Called once a saved clearance is
// found stale (expired, or Browserless still challenged with it) so the
// automated chain isn't slowed retrying a clearance that no longer works.
func (s *store) DeleteScrapeClearance(ctx context.Context, storeID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM scrape_clearances WHERE store_id = ?`, storeID)
	return err
}
