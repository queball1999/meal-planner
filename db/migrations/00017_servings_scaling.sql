-- +goose Up

-- Per-day headcount changes rescale a day's meals. Scaling always derives from
-- the as-generated ("base") yield and quantities rather than from the current
-- values, so adjusting 2 → 6 → 3 people lands on exactly the same numbers as
-- going straight to 3 and never accumulates rounding drift.

ALTER TABLE meals ADD COLUMN base_servings        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE meals ADD COLUMN base_cooked_portions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE meal_ingredients ADD COLUMN base_quantity REAL NOT NULL DEFAULT 0;

-- Backfill existing rows: whatever they hold now is their unscaled baseline.
UPDATE meals SET base_servings        = servings        WHERE base_servings = 0;
UPDATE meals SET base_cooked_portions = cooked_portions WHERE base_cooked_portions = 0;
UPDATE meal_ingredients SET base_quantity = quantity    WHERE base_quantity = 0;

-- +goose Down

ALTER TABLE meal_ingredients DROP COLUMN base_quantity;
ALTER TABLE meals DROP COLUMN base_cooked_portions;
ALTER TABLE meals DROP COLUMN base_servings;
