-- +goose Up

-- price_history logs every operator price write for an (item, store) pair, so
-- the pencil-icon price editor on the shopping list (and the item/admin price
-- forms, which share the same write path) can show how a price moved over time.
CREATE TABLE IF NOT EXISTS price_history (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id            INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    store_id           INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    price_cents        INTEGER NOT NULL,
    purchase_unit      TEXT    NOT NULL DEFAULT 'each',
    amount_per_package REAL    NOT NULL DEFAULT 1,
    recorded_by        TEXT    NOT NULL DEFAULT '',
    recorded_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_price_history_item_store ON price_history(item_id, store_id, recorded_at);

-- +goose Down

DROP TABLE IF EXISTS price_history;
