-- +goose Up

-- share_pct is roughly how much of the household's shopping happens at this
-- store (Settings → Stores, setup wizard). Only its order matters to pricing:
-- a higher-share store is tried first and wins ties, so the primary store is
-- where a line lands whenever it can answer at all. 0 everywhere (the default,
-- and every pre-existing store) means "no preference" - the old first-to-
-- answer behaviour.
ALTER TABLE stores ADD COLUMN share_pct INTEGER NOT NULL DEFAULT 0
    CHECK (share_pct BETWEEN 0 AND 100);

-- preferred_store_id is "we only buy this here": pricing looks this item up at
-- this store and nowhere else. It replaces item_store_packages.preferred
-- (00025), which could only be set for a store the item already had a package
-- row at - a household that always buys its coffee at one store had to price
-- it there by hand before it could say so. The old column stays (migrations
-- are append-only) but nothing reads it any more.
ALTER TABLE items ADD COLUMN preferred_store_id INTEGER REFERENCES stores(id) ON DELETE SET NULL;

UPDATE items SET preferred_store_id = (
    SELECT p.store_id FROM item_store_packages p
     WHERE p.item_id = items.id AND p.preferred = 1
     LIMIT 1
);

-- +goose Down
ALTER TABLE items DROP COLUMN preferred_store_id;
ALTER TABLE stores DROP COLUMN share_pct;
