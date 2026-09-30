-- +goose Up

-- need_quantity is how much the plan's recipes call for, in the line's stock
-- unit, before pantry stock is taken off - what the list shows ("4" bananas;
-- pantry_qty_used says how much of it the pantry covers).
-- buy_quantity is what that becomes once rounded up to whole store packs and
-- expressed back in the stock unit ("7.69" bananas = two 1 lb bags), which is
-- right for pricing and wrong to read. 0 means not recorded yet: a line from
-- before this column is backfilled the first time its list is viewed
-- (pricing.BackfillNeed).
ALTER TABLE shopping_list_items ADD COLUMN need_quantity REAL NOT NULL DEFAULT 0;

-- A line never matched to a real package bought exactly what it needed, less
-- what the pantry covered.
UPDATE shopping_list_items SET need_quantity = buy_quantity + pantry_qty_used WHERE pack_amount = 0;

-- +goose Down
ALTER TABLE shopping_list_items DROP COLUMN need_quantity;
