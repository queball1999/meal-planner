-- +goose Up

-- Guests do not always eat every meal of the day - a dinner party is dinner
-- only, a house guest might just be there for breakfast. guest_slots is the
-- comma-separated set of slots ("breakfast", "lunch", "dinner") the day's
-- guests are counted for. Empty string means "every slot", which is the
-- back-compatible default and what a day with guests but no explicit choice
-- means.
ALTER TABLE plan_days ADD COLUMN guest_slots TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE plan_days DROP COLUMN guest_slots;
