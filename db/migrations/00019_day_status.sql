-- +goose Up

-- Not every day of a planned week is a day you cook. Marking one "eating out"
-- or "skipped" takes it out of the shopping list and the serving maths without
-- deleting the plan around it.
--
-- Three values, not four: a day you eat leftovers is already visible as
-- leftover meal cards on that day (meals.is_leftover), so adding it here would
-- be a second, separately-editable copy of the same fact.
ALTER TABLE plan_days ADD COLUMN status TEXT NOT NULL DEFAULT 'cooking'
    CHECK(status IN ('cooking', 'eating_out', 'skipped'));

-- +goose Down

ALTER TABLE plan_days DROP COLUMN status;
