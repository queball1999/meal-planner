-- +goose Up

-- pack_amount/pack_unit capture one priced line's raw "amount per package" and
-- its unit, before reconcilePack (pricing/costing.go) converts it into the
-- ingredient's own stock unit for pack_size/buy_quantity. A guest-count or
-- meal-skip change only needs the new required quantity re-run through that
-- same pack math (pricing.RescaleShoppingList) - it must NOT ask a provider
-- again for a line that is already priced. Without the raw amount/unit that
-- rescale can't be done: pack_size/buy_quantity alone don't say whether they
-- came from a real discrete package (rescale by whole packs) or from a flat,
-- unreconciled guess for the old exact quantity (rescale by ratio instead).
-- Zero/empty means "no real pack" - the flat/never-priced case.
ALTER TABLE shopping_list_items ADD COLUMN pack_amount REAL NOT NULL DEFAULT 0;
ALTER TABLE shopping_list_items ADD COLUMN pack_unit TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE shopping_list_items DROP COLUMN pack_amount;
ALTER TABLE shopping_list_items DROP COLUMN pack_unit;
