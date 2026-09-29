-- +goose Up

-- Every sign-in attempt, for the persistent account lockout (QSS security
-- design §2.2). Keyed by the submitted username string (trimmed,
-- lowercased), not a user id, so a username that doesn't exist locks exactly
-- like one that does and "still no lockout" can't reveal which is which.
-- Timestamps are fixed-width UTC so they compare as text.
CREATE TABLE login_attempts (
    id           INTEGER PRIMARY KEY,
    username     TEXT    NOT NULL,
    client_ip    TEXT    NOT NULL,
    attempted_at TEXT    NOT NULL,
    success      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX login_attempts_pair ON login_attempts (username, client_ip, attempted_at);
CREATE INDEX login_attempts_ip   ON login_attempts (client_ip, attempted_at);
CREATE INDEX login_attempts_at   ON login_attempts (attempted_at);

-- +goose Down
DROP TABLE login_attempts;
