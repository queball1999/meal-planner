-- +goose Up

-- last_seen_at drives the server-side idle timeout (QSS security design
-- §1.4, SESSION_IDLE_MINUTES). NULL on sessions from before this migration
-- reads as created_at.
ALTER TABLE sessions ADD COLUMN last_seen_at TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN last_seen_at;
