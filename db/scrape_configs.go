package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *store) CreateScrapeConfig(ctx context.Context, p CreateScrapeConfigParams) (*ScrapeConfig, error) {
	aiAssisted := 0
	if p.AIAssisted {
		aiAssisted = 1
	}
	contextJSON := p.ContextJSON
	if contextJSON == "" {
		contextJSON = "{}"
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO scrape_configs
			(store_id, search_url_template, selectors_json, mode, ai_assisted, context_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.StoreID, p.SearchURLTemplate, p.SelectorsJSON, p.Mode, aiAssisted, contextJSON,
	)
	if err != nil {
		return nil, err
	}
	_, _ = res.LastInsertId()
	return s.GetScrapeConfigByStore(ctx, p.StoreID)
}

func (s *store) GetScrapeConfigByStore(ctx context.Context, storeID int64) (*ScrapeConfig, error) {
	var sc ScrapeConfig
	var createdAt string
	var aiAssisted int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, store_id, search_url_template, selectors_json, mode, ai_assisted,
		       status, last_tested_at, created_at, context_json,
		       block_reason, block_url, blocked_at
		FROM scrape_configs WHERE store_id = ?`, storeID).
		Scan(&sc.ID, &sc.StoreID, &sc.SearchURLTemplate, &sc.SelectorsJSON,
			&sc.Mode, &aiAssisted, &sc.Status, &sc.LastTestedAt, &createdAt, &sc.ContextJSON,
			&sc.BlockReason, &sc.BlockURL, &sc.BlockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sc.AIAssisted = aiAssisted != 0
	sc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &sc, nil
}

func (s *store) ListScrapeConfigs(ctx context.Context) ([]*ScrapeConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, store_id, search_url_template, selectors_json, mode, ai_assisted,
		       status, last_tested_at, created_at, context_json,
		       block_reason, block_url, blocked_at
		FROM scrape_configs ORDER BY store_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ScrapeConfig
	for rows.Next() {
		var sc ScrapeConfig
		var createdAt string
		var aiAssisted int
		if err := rows.Scan(&sc.ID, &sc.StoreID, &sc.SearchURLTemplate, &sc.SelectorsJSON,
			&sc.Mode, &aiAssisted, &sc.Status, &sc.LastTestedAt, &createdAt, &sc.ContextJSON,
			&sc.BlockReason, &sc.BlockURL, &sc.BlockedAt); err != nil {
			return nil, err
		}
		sc.AIAssisted = aiAssisted != 0
		sc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, &sc)
	}
	return out, rows.Err()
}

// MarkScrapeConfigBlocked implements Store.
func (s *store) MarkScrapeConfigBlocked(ctx context.Context, storeID int64, reason, blockedURL string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE scrape_configs SET
			block_reason = ?,
			block_url    = ?,
			blocked_at   = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE store_id = ?`,
		reason, blockedURL, storeID,
	)
	return err
}

// ClearScrapeConfigBlocked implements Store.
func (s *store) ClearScrapeConfigBlocked(ctx context.Context, storeID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE scrape_configs SET block_reason = '', block_url = '', blocked_at = ''
		WHERE store_id = ?`,
		storeID,
	)
	return err
}

func (s *store) UpdateScrapeConfig(ctx context.Context, p UpdateScrapeConfigParams) error {
	aiAssisted := 0
	if p.AIAssisted {
		aiAssisted = 1
	}
	contextJSON := p.ContextJSON
	if contextJSON == "" {
		contextJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE scrape_configs SET
			search_url_template = ?,
			selectors_json      = ?,
			mode                = ?,
			ai_assisted         = ?,
			status              = ?,
			last_tested_at      = ?,
			context_json        = ?
		WHERE id = ?`,
		p.SearchURLTemplate, p.SelectorsJSON, p.Mode, aiAssisted, p.Status, p.LastTestedAt,
		contextJSON, p.ID,
	)
	return err
}
