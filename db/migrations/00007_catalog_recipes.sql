-- +goose Up
CREATE TABLE IF NOT EXISTS catalog_recipes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id  INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    title         TEXT    NOT NULL,
    source_kind   TEXT    NOT NULL DEFAULT 'manual',  -- 'ai'|'imported'|'manual'
    source_url    TEXT    NOT NULL DEFAULT '',
    source_site   TEXT    NOT NULL DEFAULT '',
    image_path    TEXT    NOT NULL DEFAULT '',         -- relative path under RECIPE_IMAGE_DIR
    servings      INTEGER NOT NULL DEFAULT 4,
    prep_minutes  INTEGER NOT NULL DEFAULT 0,
    cook_minutes  INTEGER NOT NULL DEFAULT 0,
    tags          TEXT    NOT NULL DEFAULT '[]',       -- JSON array of strings
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS catalog_recipe_ingredients (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_recipe_id INTEGER NOT NULL REFERENCES catalog_recipes(id) ON DELETE CASCADE,
    name              TEXT    NOT NULL,
    quantity          TEXT    NOT NULL DEFAULT '',
    unit              TEXT    NOT NULL DEFAULT '',
    normalized_term   TEXT    NOT NULL DEFAULT '',
    position          INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS catalog_recipe_steps (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_recipe_id INTEGER NOT NULL REFERENCES catalog_recipes(id) ON DELETE CASCADE,
    position          INTEGER NOT NULL,
    text              TEXT    NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS catalog_recipe_steps;
DROP TABLE IF EXISTS catalog_recipe_ingredients;
DROP TABLE IF EXISTS catalog_recipes;
