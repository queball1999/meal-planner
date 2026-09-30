-- +goose Up

-- llm_calls is the LLM half of the admin Audit Log: one row per Generate call
-- with its prompt and response. It replaces a 30-entry in-memory ring that
-- lost plan-generation and chat calls - a plan run's price estimates pushed
-- them out within seconds, saving an AI Provider setting swapped in an empty
-- ring, and a restart wiped it. db.InsertLLMCall keeps the newest
-- LLMCallKeep rows.
CREATE TABLE llm_calls (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id  INTEGER,
    purpose       TEXT    NOT NULL DEFAULT '',
    provider      TEXT    NOT NULL DEFAULT '',
    model         TEXT    NOT NULL DEFAULT '',
    system        TEXT    NOT NULL DEFAULT '',
    prompt        TEXT    NOT NULL DEFAULT '',
    response      TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE llm_calls;
