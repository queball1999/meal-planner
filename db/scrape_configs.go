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
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO scrape_configs
			(store_id, search_url_template, selectors_json, mode, ai_assisted)
		VALUES (?, ?, ?, ?, ?)`,
		p.StoreID, p.SearchURLTemplate, p.SelectorsJSON, p.Mode, aiAssisted,
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
		       status, last_tested_at, created_at
		FROM scrape_configs WHERE store_id = ?`, storeID).
		Scan(&sc.ID, &sc.StoreID, &sc.SearchURLTemplate, &sc.SelectorsJSON,
			&sc.Mode, &aiAssisted, &sc.Status, &sc.LastTestedAt, &createdAt)
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
		       status, last_tested_at, created_at
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
			&sc.Mode, &aiAssisted, &sc.Status, &sc.LastTestedAt, &createdAt); err != nil {
			return nil, err
		}
		sc.AIAssisted = aiAssisted != 0
		sc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, &sc)
	}
	return out, rows.Err()
}

func (s *store) UpdateScrapeConfig(ctx context.Context, p UpdateScrapeConfigParams) error {
	aiAssisted := 0
	if p.AIAssisted {
		aiAssisted = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE scrape_configs SET
			search_url_template = ?,
			selectors_json      = ?,
			mode                = ?,
			ai_assisted         = ?,
			status              = ?,
			last_tested_at      = ?
		WHERE id = ?`,
		p.SearchURLTemplate, p.SelectorsJSON, p.Mode, aiAssisted, p.Status, p.LastTestedAt, p.ID,
	)
	return err
}
