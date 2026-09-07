-- +goose Up

-- secrets holds encrypted-at-rest credentials, separate from the plaintext
-- settings table. ciphertext is a cryptbox "enc:v1:…" string keyed off
-- SESSION_SECRET. Currently used for the Home Assistant long-lived token.
CREATE TABLE IF NOT EXISTS secrets (
    key        TEXT PRIMARY KEY,
    ciphertext TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS secrets;
