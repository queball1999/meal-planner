-- +goose Up

-- "We buy this here" (Settings → Stores): a household can prefer one store for
-- an item even though it has package/price rows at several. Lives on the
-- (item, store) package row rather than a new table, since a preference with
-- no purchase unit or price recorded for that store would not mean anything -
-- you can only prefer a store for an item once you have told Go Eat that the
-- item is sold there at all.
ALTER TABLE item_store_packages ADD COLUMN preferred INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE item_store_packages DROP COLUMN preferred;
