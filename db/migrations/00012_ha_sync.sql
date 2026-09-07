-- +goose Up

-- ha_sync_map bridges a household's shopping-list items to entries in a Home
-- Assistant to-do list. It is keyed by (household, normalized_term) rather than
-- shopping_list_items.id because that table is wiped and rebuilt on every plan
-- re-cost, whereas the HA list and this mapping should survive across weeks.
CREATE TABLE IF NOT EXISTS ha_sync_map (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id    INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    item_id         INTEGER REFERENCES items(id) ON DELETE SET NULL,
    normalized_term TEXT    NOT NULL,
    ha_uid          TEXT    NOT NULL DEFAULT '',   -- HA to-do item uid, once known
    ha_summary      TEXT    NOT NULL DEFAULT '',   -- the text pushed to HA
    ha_status       TEXT    NOT NULL DEFAULT '',   -- 'needs_action' | 'completed'
    local_checked   INTEGER NOT NULL DEFAULT 0,    -- last checked state we reconciled
    last_pushed_at  TEXT    NOT NULL DEFAULT '',
    last_pulled_at  TEXT    NOT NULL DEFAULT '',
    UNIQUE(household_id, normalized_term)
);

CREATE INDEX IF NOT EXISTS idx_ha_sync_map_household ON ha_sync_map(household_id);

-- +goose Down
DROP TABLE IF EXISTS ha_sync_map;
