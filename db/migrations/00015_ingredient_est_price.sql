-- +goose Up

-- The meal-planning LLM is now required to return a rough US grocery price for
-- each ingredient at generation time (see plan.systemPrompt). Keeping it lets
-- the pricing chain fall back to a real number instead of $0 when every live
-- provider comes up empty for an ingredient (§6.4 costing.go).
ALTER TABLE meal_ingredients ADD COLUMN est_price_cents INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE meal_ingredients DROP COLUMN est_price_cents;
