-- +goose Up
-- Records when a store's scraper hit a genuine bot wall (Cloudflare/Incapsula/
-- PerimeterX/CAPTCHA signature) even after the full automated FlareSolverr +
-- Browserless chain ran - distinct from "degraded" (selectors found nothing,
-- or the site had no products), which is not something a human solving a
-- CAPTCHA would fix. See scrape.IsBotWall / pricing.ScraperProvider.Lookup.
ALTER TABLE scrape_configs ADD COLUMN block_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE scrape_configs ADD COLUMN block_url TEXT NOT NULL DEFAULT '';
ALTER TABLE scrape_configs ADD COLUMN blocked_at TEXT NOT NULL DEFAULT '';

-- Cookies (+ the user agent they were issued to) an admin obtained by solving
-- a store's challenge themselves - either live, through the CDP session in
-- scrape/live, or pasted in manually. Reused by ScraperProvider.Lookup ahead
-- of the automated chain until expires_at, one row per store.
CREATE TABLE scrape_clearances (
    store_id     INTEGER PRIMARY KEY REFERENCES scrape_configs(store_id) ON DELETE CASCADE,
    cookies_json TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    obtained_at  TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);

-- +goose Down
DROP TABLE scrape_clearances;
ALTER TABLE scrape_configs DROP COLUMN blocked_at;
ALTER TABLE scrape_configs DROP COLUMN block_url;
ALTER TABLE scrape_configs DROP COLUMN block_reason;
