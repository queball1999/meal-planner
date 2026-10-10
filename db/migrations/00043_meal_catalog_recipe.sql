-- +goose Up

-- catalog_recipe_id ties a meal to the saved recipe it was made from, so an
-- edit to that recipe can be carried onto every plan cooking it
-- (plan.SyncRecipeMeals). A meal is a scaled copy of its recipe - title, steps
-- and ingredients are written onto the meal's own rows - and until now nothing
-- recorded which recipe that was, so an edited recipe never reached the plan
-- or its shopping list. NULL for a meal with no recipe behind it (leftovers, a
-- meal rewritten by budget repair).
--
-- No REFERENCES clause, as with llm_calls.plan_id (00042): catalog_recipes ids
-- are AUTOINCREMENT and never reused, so a link left behind by a deleted
-- recipe matches nothing and is harmless.
ALTER TABLE meals ADD COLUMN catalog_recipe_id INTEGER;
CREATE INDEX idx_meals_catalog_recipe ON meals(catalog_recipe_id);

-- Backfill by title within the household: that is the identity plan generation
-- already uses when it files a meal in the catalog (one recipe per title).
UPDATE meals
SET catalog_recipe_id = (
    SELECT cr.id
    FROM catalog_recipes cr
    JOIN plans p ON p.id = meals.plan_id
    WHERE cr.household_id = p.household_id
      AND cr.title = meals.title COLLATE NOCASE
    ORDER BY cr.id
    LIMIT 1
)
WHERE is_leftover = 0;

-- +goose Down
DROP INDEX idx_meals_catalog_recipe;
ALTER TABLE meals DROP COLUMN catalog_recipe_id;
