-- +goose Up

-- How much of a shopping line the pantry already covered.
--
-- Recorded rather than just subtracted, so the list can say "1 of 3 lb from
-- your pantry" instead of silently showing a smaller number. A quantity that
-- shrinks with no explanation looks like a bug in the plan, and the one thing
-- a budget-first shopping list has to be is legible.
--
-- Zero means the pantry contributed nothing, which is also the correct value
-- for every row written before this column existed.
ALTER TABLE shopping_list_items ADD COLUMN pantry_qty_used REAL NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE shopping_list_items DROP COLUMN pantry_qty_used;
