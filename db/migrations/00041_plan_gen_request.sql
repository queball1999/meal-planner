-- +goose Up

-- gen_request is the generate dialog's submission, as JSON (db.PlanRequest):
-- the recipes picked, the free-text asks, the on-hand rows and the scope. Kept
-- with the plan so the same request can be looked at afterwards and sent again
-- for another week. Before this only its flattened prompt strings survived,
-- and only until the LLM log rotated them out. '' for a plan built by hand,
-- by the auto-plan scheduler, or before this column existed.
ALTER TABLE plans ADD COLUMN gen_request TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE plans DROP COLUMN gen_request;
