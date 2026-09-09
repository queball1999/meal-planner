-- +goose Up

-- pending marks a shopping-list line as a skeleton row CostPlan has seeded
-- but not yet resolved a real price for (pricing.SeedShoppingList /
-- ResolvePricing): the shopping list tab renders these as loading
-- placeholders instead of a price while pricing runs in the background after
-- a plan is generated. price_source/confidence stay 'estimate' for a pending
-- row (their CHECK constraints do not allow a new value without a full table
-- rebuild), so this is a separate flag rather than a new enum value.
ALTER TABLE shopping_list_items ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE shopping_list_items DROP COLUMN pending;
