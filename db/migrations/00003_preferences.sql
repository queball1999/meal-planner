-- +goose Up

CREATE TABLE IF NOT EXISTS stores (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id    INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    kind            TEXT    NOT NULL DEFAULT 'grocery'
                            CHECK(kind IN ('grocery','warehouse','specialty','online')),
    provider_chain  TEXT    NOT NULL DEFAULT 'cache,manual,ai_estimate',
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS preferences (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id        INTEGER NOT NULL UNIQUE REFERENCES households(id) ON DELETE CASCADE,
    diet_tags           TEXT    NOT NULL DEFAULT '[]',
    cuisines            TEXT    NOT NULL DEFAULT '[]',
    dislikes            TEXT    NOT NULL DEFAULT '[]',
    leftover_tolerance  INTEGER NOT NULL DEFAULT 1,
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS allergies (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    term         TEXT    NOT NULL,
    UNIQUE(household_id, term)
);

CREATE TABLE IF NOT EXISTS meal_slot_hints (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    slot         TEXT    NOT NULL CHECK(slot IN ('breakfast','lunch','dinner')),
    raw_text     TEXT    NOT NULL DEFAULT '',
    parsed_json  TEXT    NOT NULL DEFAULT 'null',
    effort       TEXT    NOT NULL DEFAULT 'standard'
                         CHECK(effort IN ('quick','standard','elaborate')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(household_id, slot)
);

CREATE TABLE IF NOT EXISTS meal_feedback (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id  INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    meal_id       INTEGER,
    title         TEXT    NOT NULL,
    rating        INTEGER NOT NULL CHECK(rating IN (-1, 1)),
    tags_snapshot TEXT    NOT NULL DEFAULT '[]',
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS ai_runs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id      INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    purpose           TEXT    NOT NULL DEFAULT 'generation',
    provider          TEXT    NOT NULL DEFAULT '',
    model             TEXT    NOT NULL DEFAULT '',
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    est_cost_cents    INTEGER NOT NULL DEFAULT 0,
    status            TEXT    NOT NULL DEFAULT 'ok',
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down

DROP TABLE IF EXISTS ai_runs;
DROP TABLE IF EXISTS meal_feedback;
DROP TABLE IF EXISTS meal_slot_hints;
DROP TABLE IF EXISTS allergies;
DROP TABLE IF EXISTS preferences;
DROP TABLE IF EXISTS stores;
