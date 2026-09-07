-- +goose Up

-- catalog.EnsureItem creates a new item whenever a normalized ingredient name
-- does not match one exactly, so "chicken breast" and "chicken breasts" become
-- two separate items with separate prices and separate pantry stock. Aliases
-- are the second lookup that stops that: a term the household has already said
-- means an existing item resolves to it instead of creating a near-duplicate.
--
-- The alias is stored normalized (pricing.Normalize), the same shape as
-- items.normalized_term, because that is what the lookup has in hand.

CREATE TABLE IF NOT EXISTS item_aliases (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    item_id      INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    alias        TEXT    NOT NULL,
    -- 'manual' when a person confirmed the match in the shopping list dialog,
    -- 'auto' when a fuzzy match was confident enough to take on its own. Kept
    -- so a bad automatic match can be found and undone without also throwing
    -- away everything the household confirmed by hand.
    source       TEXT    NOT NULL DEFAULT 'manual'
                         CHECK(source IN ('manual', 'auto')),
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- One meaning per term per household: an alias that pointed at two items
    -- would make the lookup order-dependent.
    UNIQUE(household_id, alias)
);

CREATE INDEX IF NOT EXISTS idx_item_aliases_item ON item_aliases(item_id);

-- +goose Down

DROP INDEX IF EXISTS idx_item_aliases_item;
DROP TABLE IF EXISTS item_aliases;
