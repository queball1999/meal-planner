-- +goose Up

-- plan_id ties an LLM call to the plan generation it ran for (llm.WithPlan),
-- so the generation progress screen's debug panel can show that run's calls
-- alone instead of whatever the instance made most recently. NULL for every
-- call made outside a generation; the Audit Log still lists all of them.
ALTER TABLE llm_calls ADD COLUMN plan_id INTEGER;
CREATE INDEX idx_llm_calls_plan ON llm_calls(plan_id);

-- +goose Down
DROP INDEX idx_llm_calls_plan;
ALTER TABLE llm_calls DROP COLUMN plan_id;
