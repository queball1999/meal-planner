-- +goose Up

-- price_cache is the system-of-record for every price ever obtained (§6.0, §6.5).
-- API hits, scraper results, and AI estimates all land here, each tagged with
-- their source and confidence so history stays honest.
CREATE TABLE IF NOT EXISTS price_cache (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    store_id        INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    normalized_term TEXT    NOT NULL,
    price_cents     INTEGER NOT NULL,
    purchase_unit   TEXT    NOT NULL DEFAULT '',
    pack_size       REAL    NOT NULL DEFAULT 1,
    source          TEXT    NOT NULL CHECK(source IN ('live','cache','manual','scrape','estimate')),
    confidence      TEXT    NOT NULL CHECK(confidence IN ('live','cached','manual','scrape','estimate')),
    fetched_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(store_id, normalized_term)  -- upsert replaces on conflict
);

-- manual_prices holds operator-entered prices per store + region (§6.2 ManualProvider).
-- Region is the household ZIP/metro — not a FK; stored as plain text.
CREATE TABLE IF NOT EXISTS manual_prices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    store_id        INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    region          TEXT    NOT NULL DEFAULT '',
    normalized_term TEXT    NOT NULL,
    price_cents     INTEGER NOT NULL,
    pack_size       REAL    NOT NULL DEFAULT 1,
    purchase_unit   TEXT    NOT NULL DEFAULT '',
    updated_by      TEXT    NOT NULL DEFAULT '',
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(store_id, region, normalized_term)
);

-- item_product_map caches the chosen product for a (store, normalized_term) pair (§6.3).
-- Avoids re-querying providers for the same ingredient within/across weeks.
CREATE TABLE IF NOT EXISTS item_product_map (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    store_id        INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    normalized_term TEXT    NOT NULL,
    chosen_product  TEXT    NOT NULL DEFAULT '',
    pack_size       REAL    NOT NULL DEFAULT 1,
    purchase_unit   TEXT    NOT NULL DEFAULT '',
    barcode         TEXT    NOT NULL DEFAULT '',
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(store_id, normalized_term)
);

-- scrape_configs holds per-store scraping configuration built by the §6.7 tool.
-- selectors_json is a JSON object: {name, price, pack_size, availability} — each
-- field has a css/xpath selector + optional regex.
CREATE TABLE IF NOT EXISTS scrape_configs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    store_id            INTEGER NOT NULL UNIQUE REFERENCES stores(id) ON DELETE CASCADE,
    search_url_template TEXT    NOT NULL DEFAULT '',  -- {term} placeholder
    selectors_json      TEXT    NOT NULL DEFAULT '{}',
    mode                TEXT    NOT NULL DEFAULT 'assisted'
                                CHECK(mode IN ('assisted','auto','auto_ai')),
    ai_assisted         INTEGER NOT NULL DEFAULT 0,
    status              TEXT    NOT NULL DEFAULT 'active'
                                CHECK(status IN ('active','degraded','unconfigured')),
    last_tested_at      TEXT    NOT NULL DEFAULT '',
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- shopping_list_items are the priced, consolidated buy lines for a plan (§5.1, §6.4).
-- meal_ingredient_refs is a JSON array of meal_ingredient IDs that rolled up into
-- this line (for cross-meal reuse tracking).
CREATE TABLE IF NOT EXISTS shopping_list_items (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    plan_id              INTEGER NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    store_id             INTEGER REFERENCES stores(id) ON DELETE SET NULL,
    meal_ingredient_refs TEXT    NOT NULL DEFAULT '[]',  -- JSON []int64
    display_name         TEXT    NOT NULL DEFAULT '',
    buy_quantity         REAL    NOT NULL DEFAULT 0,
    pack_size            REAL    NOT NULL DEFAULT 1,
    purchase_unit        TEXT    NOT NULL DEFAULT '',
    unit_price_cents     INTEGER NOT NULL DEFAULT 0,
    line_total_cents     INTEGER NOT NULL DEFAULT 0,
    price_source         TEXT    NOT NULL DEFAULT 'estimate'
                                 CHECK(price_source IN ('live','cache','manual','scrape','estimate')),
    confidence           TEXT    NOT NULL DEFAULT 'estimate'
                                 CHECK(confidence IN ('live','cached','manual','scrape','estimate')),
    checked              INTEGER NOT NULL DEFAULT 0,
    in_pantry            INTEGER NOT NULL DEFAULT 0
);

-- +goose Down

DROP TABLE IF EXISTS shopping_list_items;
DROP TABLE IF EXISTS scrape_configs;
DROP TABLE IF EXISTS item_product_map;
DROP TABLE IF EXISTS manual_prices;
DROP TABLE IF EXISTS price_cache;
