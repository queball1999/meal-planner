-- +goose Up

-- A day's headcount was only ever the household's own members. Guests are the
-- people on top of that - a dinner party, a friend staying over - and each one
-- eats a standard portion. Stored as a count rather than folded into portions
-- so the plan page can re-render the "Guests x N" control with its number, and
-- so editing a member's portion factor later does not silently restate how
-- many guests a past day had.
ALTER TABLE plan_days ADD COLUMN guests INTEGER NOT NULL DEFAULT 0
    CHECK(guests >= 0 AND guests <= 50);

-- +goose Down

ALTER TABLE plan_days DROP COLUMN guests;
