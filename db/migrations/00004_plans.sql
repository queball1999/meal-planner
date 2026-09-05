-- +goose Up

CREATE TABLE IF NOT EXISTS plans (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id        INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    week_start          TEXT    NOT NULL,  -- ISO date YYYY-MM-DD
    week_end            TEXT    NOT NULL,  -- ISO date YYYY-MM-DD
    budget_cents        INTEGER NOT NULL DEFAULT 0,
    total_cents         INTEGER NOT NULL DEFAULT 0,
    confidence_summary  TEXT    NOT NULL DEFAULT '',
    status              TEXT    NOT NULL DEFAULT 'generating'
                                CHECK(status IN ('generating','ready','error')),
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS plan_days (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    plan_id   INTEGER NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    date      TEXT    NOT NULL,  -- ISO date YYYY-MM-DD
    headcount INTEGER NOT NULL DEFAULT 2,
    note      TEXT    NOT NULL DEFAULT '',
    UNIQUE(plan_id, date)
);

CREATE TABLE IF NOT EXISTS meals (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    plan_id                 INTEGER NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    day                     TEXT    NOT NULL,  -- ISO date YYYY-MM-DD
    slot                    TEXT    NOT NULL CHECK(slot IN ('breakfast','lunch','dinner')),
    title                   TEXT    NOT NULL DEFAULT '',
    effort                  TEXT    NOT NULL DEFAULT 'standard'
                                    CHECK(effort IN ('quick','standard','elaborate')),
    servings                INTEGER NOT NULL DEFAULT 2,
    cooked_portions         INTEGER NOT NULL DEFAULT 2,
    is_leftover             INTEGER NOT NULL DEFAULT 0,
    leftover_source_meal_id INTEGER REFERENCES meals(id),
    locked                  INTEGER NOT NULL DEFAULT 0,
    ai_run_id               INTEGER REFERENCES ai_runs(id),
    UNIQUE(plan_id, day, slot)
);

CREATE TABLE IF NOT EXISTS meal_recipes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    meal_id    INTEGER NOT NULL UNIQUE REFERENCES meals(id) ON DELETE CASCADE,
    steps_json TEXT    NOT NULL DEFAULT '[]',
    servings   INTEGER NOT NULL DEFAULT 2,
    notes      TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS meal_ingredients (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    meal_id         INTEGER NOT NULL REFERENCES meals(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    quantity        REAL    NOT NULL DEFAULT 0,
    unit            TEXT    NOT NULL DEFAULT '',
    normalized_term TEXT    NOT NULL DEFAULT ''
);

-- +goose Down

DROP TABLE IF EXISTS meal_ingredients;
DROP TABLE IF EXISTS meal_recipes;
DROP TABLE IF EXISTS meals;
DROP TABLE IF EXISTS plan_days;
DROP TABLE IF EXISTS plans;
