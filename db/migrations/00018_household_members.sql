-- +goose Up

-- A household was only ever a headcount, so a plan for "4" budgeted the same
-- food whether that was four adults or two adults and two toddlers. Named
-- members each carry a portion factor - how much that person eats relative to
-- one standard adult serving - and the plan's serving maths moves from a count
-- of people to a sum of those factors.

CREATE TABLE IF NOT EXISTS household_members (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id   INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    name           TEXT    NOT NULL,
    -- 1.0 is one standard adult serving. A small child sits near 0.5, a light
    -- eater 0.8, a big eater 1.4. Bounded because a typo of 100 in this field
    -- would quietly generate a shopping list for a restaurant.
    portion_factor REAL    NOT NULL DEFAULT 1.0
                           CHECK(portion_factor > 0 AND portion_factor <= 5),
    -- Free text the generator gets alongside the household's own preferences:
    -- "no shellfish", "packs lunch on Tuesdays", "toddler - soft textures".
    notes          TEXT    NOT NULL DEFAULT '',
    sort_order     INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(household_id, name)
);

CREATE INDEX IF NOT EXISTS idx_household_members_household
    ON household_members(household_id, sort_order);

-- Which members are eating on a given day, and the portion total that implies.
--
-- The total is stored rather than recomputed from member_ids on read: editing
-- a member's portion factor next month must not silently restate what a plan
-- from last month was scaled to. plan_days.headcount stays as the displayed
-- count of people.
ALTER TABLE plan_days ADD COLUMN member_ids TEXT NOT NULL DEFAULT '[]';
ALTER TABLE plan_days ADD COLUMN portions   REAL NOT NULL DEFAULT 0;

-- Existing days were scaled to a plain headcount, which is exactly that many
-- standard portions.
UPDATE plan_days SET portions = headcount WHERE portions = 0;

-- Seed one standard-portion member per existing household head, so a household
-- created before this migration keeps the same serving maths it had. Named
-- generically because there is nothing on record to name them from; the
-- preferences page is where they get real names.
WITH RECURSIVE seq(i) AS (
    SELECT 1
    UNION ALL
    SELECT i + 1 FROM seq WHERE i < 20
)
INSERT INTO household_members (household_id, name, portion_factor, sort_order)
SELECT h.id, 'Person ' || s.i, 1.0, s.i
FROM households h
JOIN seq s ON s.i <= h.household_size
WHERE NOT EXISTS (
    SELECT 1 FROM household_members m WHERE m.household_id = h.id
);

-- +goose Down

ALTER TABLE plan_days DROP COLUMN portions;
ALTER TABLE plan_days DROP COLUMN member_ids;
DROP INDEX IF EXISTS idx_household_members_household;
DROP TABLE IF EXISTS household_members;
