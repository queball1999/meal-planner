-- +goose Up

-- items is the household's canonical grocery-item catalog (§6.3 extended).
-- Before this table every ingredient was only a free-text name plus a derived
-- normalized_term string; items gives that term a real row with metadata:
-- category, photo, a canonical "stock unit" it is aggregated/held in, and a
-- default purchase quantity. meal_ingredients, pantry_items, shopping_list_items,
-- price_cache, and manual_prices gain an item_id FK pointing here.
CREATE TABLE IF NOT EXISTS items (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id         INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name                 TEXT    NOT NULL,
    normalized_term      TEXT    NOT NULL,
    category             TEXT    NOT NULL DEFAULT '',
    stock_unit           TEXT    NOT NULL DEFAULT 'each',   -- unit the item is aggregated/held in (Grocy QU_STOCK)
    default_purchase_qty REAL    NOT NULL DEFAULT 1,
    image_path           TEXT    NOT NULL DEFAULT '',       -- relative path under ITEM_IMAGE_DIR
    image_attribution    TEXT    NOT NULL DEFAULT '',       -- required credit line for the photo
    image_source_url     TEXT    NOT NULL DEFAULT '',       -- where the photo is lazily fetched from
    source               TEXT    NOT NULL DEFAULT 'auto'
                                 CHECK(source IN ('builtin','manual','auto')),
    notes                TEXT    NOT NULL DEFAULT '',
    created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(household_id, normalized_term)
);

-- unit_conversions is the quantity-conversion graph (Grocy QU conversions).
-- A row means: 1 <from_unit> = <factor> <to_unit>. item_id NULL is a global
-- conversion (mass: g/kg/oz/lb; volume: ml/l/tsp/tbsp/cup/fl-oz). A row with an
-- item_id is an item-specific bridge, usually count -> mass/volume
-- (e.g. 1 clove garlic = 3 g, 1 egg = 50 g).
CREATE TABLE IF NOT EXISTS unit_conversions (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id   INTEGER REFERENCES items(id) ON DELETE CASCADE,  -- NULL = global
    from_unit TEXT    NOT NULL,
    to_unit   TEXT    NOT NULL,
    factor    REAL    NOT NULL,
    UNIQUE(item_id, from_unit, to_unit)
);

-- item_store_packages is the first-class per-store package definition: how the
-- item is sold at one store and for how much. Supersedes the pack_size /
-- purchase_unit scalars carried on manual_prices / price_cache, which stay as a
-- fallback for un-catalogued terms.
CREATE TABLE IF NOT EXISTS item_store_packages (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id            INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    store_id           INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    purchase_unit      TEXT    NOT NULL DEFAULT 'each',
    amount_per_package REAL    NOT NULL DEFAULT 1,          -- in purchase_unit (Grocy "amount per package")
    price_cents        INTEGER NOT NULL DEFAULT 0,
    updated_by         TEXT    NOT NULL DEFAULT '',
    updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(item_id, store_id)
);

CREATE INDEX IF NOT EXISTS idx_unit_conversions_item     ON unit_conversions(item_id);
CREATE INDEX IF NOT EXISTS idx_item_store_packages_item  ON item_store_packages(item_id);
CREATE INDEX IF NOT EXISTS idx_item_store_packages_store ON item_store_packages(store_id);

-- item_id back-references. Nullable, ON DELETE SET NULL: deleting a catalog item
-- must not cascade-delete plan/pantry history, it just unlinks.
ALTER TABLE meal_ingredients    ADD COLUMN item_id INTEGER REFERENCES items(id) ON DELETE SET NULL;
ALTER TABLE pantry_items        ADD COLUMN item_id INTEGER REFERENCES items(id) ON DELETE SET NULL;
ALTER TABLE shopping_list_items ADD COLUMN item_id INTEGER REFERENCES items(id) ON DELETE SET NULL;
ALTER TABLE price_cache         ADD COLUMN item_id INTEGER REFERENCES items(id) ON DELETE SET NULL;
ALTER TABLE manual_prices       ADD COLUMN item_id INTEGER REFERENCES items(id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE manual_prices       DROP COLUMN item_id;
ALTER TABLE price_cache         DROP COLUMN item_id;
ALTER TABLE shopping_list_items DROP COLUMN item_id;
ALTER TABLE pantry_items        DROP COLUMN item_id;
ALTER TABLE meal_ingredients    DROP COLUMN item_id;

DROP TABLE IF EXISTS item_store_packages;
DROP TABLE IF EXISTS unit_conversions;
DROP TABLE IF EXISTS items;
