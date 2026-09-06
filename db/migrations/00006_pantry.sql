-- +goose Up

-- pantry_items tracks what the household has on hand (§5.4, §10.1).
-- normalized_term joins to meal_ingredients and price_cache for reuse matching.
-- barcode is populated when an item is scanned in (§8.4d); empty is fine.
CREATE TABLE IF NOT EXISTS pantry_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id     INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name             TEXT    NOT NULL,
    normalized_term  TEXT    NOT NULL DEFAULT '',
    quantity_on_hand REAL    NOT NULL DEFAULT 0,
    unit             TEXT    NOT NULL DEFAULT 'each',
    barcode          TEXT    NOT NULL DEFAULT '',
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(household_id, normalized_term)
);

-- +goose Down

DROP TABLE IF EXISTS pantry_items;
