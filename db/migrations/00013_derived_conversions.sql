-- +goose Up

-- derived marks a unit_conversions row as machine-generated: a flattened one-hop
-- edge from some unit to an item's stock unit, precomputed by
-- pricing.AutoConversions so costing and the item page never have to BFS the
-- conversion graph. derived = 0 rows are hand-entered or seeded and are never
-- touched by the recalc; derived = 1 rows are wiped and rebuilt whenever the
-- item's stock unit, default buy qty, or hand-entered conversions change.
ALTER TABLE unit_conversions ADD COLUMN derived INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_unit_conversions_item_derived
    ON unit_conversions(item_id, derived);

-- +goose Down

DROP INDEX IF EXISTS idx_unit_conversions_item_derived;
ALTER TABLE unit_conversions DROP COLUMN derived;
