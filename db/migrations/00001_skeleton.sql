-- +goose Up
-- +goose StatementBegin

-- settings: runtime-editable configuration. Env-seeded on first start with
-- ON CONFLICT DO NOTHING so restarts never revert operator changes (§11).
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    source     TEXT NOT NULL DEFAULT 'default', -- 'default' | 'env' | 'ui'
    secret     INTEGER NOT NULL DEFAULT 0,      -- 1 = write-only in UI, logged as "(set)"
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- app_events: audit log — always-on even on LAN (§9.3).
CREATE TABLE IF NOT EXISTS app_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    actor_user_id INTEGER,
    actor_label TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    metadata    TEXT,                           -- JSON blob
    ip_address  TEXT,
    user_agent  TEXT,
    status      TEXT    NOT NULL DEFAULT 'ok'
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS app_events;
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd
