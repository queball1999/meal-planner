-- +goose Up
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT    NOT NULL DEFAULT '',
    source     TEXT    NOT NULL DEFAULT 'env',  -- 'env' (seeded from .env) | 'admin' (edited in Settings)
    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
