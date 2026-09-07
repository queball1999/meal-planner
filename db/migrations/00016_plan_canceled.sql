-- +goose Up

-- Regenerating a plan for a week that already has one no longer deletes the
-- old plan - it gets flagged canceled instead, so it stays in /plan/history
-- for archival purposes while GetLatestPlan/GetPlanByWeekStart skip it in
-- favor of the new one.
ALTER TABLE plans ADD COLUMN canceled INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE plans DROP COLUMN canceled;
