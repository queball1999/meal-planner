-- +goose Up

-- Per-meal cooking status (Plan page: icons on the meal card itself, replacing
-- the day-level dropdown). Shares plan_days.status's vocabulary (cooking /
-- eating_out / skipped, see db.ValidDayStatus) so a meal can be skipped or
-- marked eating-out individually - "cooking" everything else that day, but
-- eating out for dinner - rather than only at whole-day granularity.
--
-- plan_days.status and its dropdown-era plumbing (SetPlanDayStatus, the
-- /plan/days/{date}/status routes) are left in place: existing plans still
-- carry it, and ListIngredientsByPlan now excludes a line when EITHER the
-- meal or its day is marked non-cooking, so nothing that already relied on
-- day-level status stops working. Going forward the UI only ever sets the new
-- per-meal column.
ALTER TABLE meals ADD COLUMN status TEXT NOT NULL DEFAULT 'cooking';

-- +goose Down

ALTER TABLE meals DROP COLUMN status;
