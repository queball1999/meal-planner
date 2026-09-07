-- +goose Up

-- Conversation history for the site-wide assistant.
--
-- Stored rather than kept in the browser: the assistant makes real changes to
-- the household's plan, and "what did I ask it to do, and what did it say it
-- did" has to survive a refresh, a different device, and the next person in
-- the household asking why Tuesday moved.

CREATE TABLE IF NOT EXISTS chat_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    role         TEXT    NOT NULL CHECK(role IN ('user', 'assistant')),
    content      TEXT    NOT NULL,
    -- The tool calls behind one assistant turn, as a JSON array of
    -- {tool, args, summary, error}. Kept so the UI can show the work rather
    -- than only the summary - an assistant that says "done" and shows nothing
    -- is not auditable.
    audit_json   TEXT    NOT NULL DEFAULT '[]',
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_household
    ON chat_messages(household_id, id);

-- +goose Down

DROP INDEX IF EXISTS idx_chat_messages_household;
DROP TABLE IF EXISTS chat_messages;
