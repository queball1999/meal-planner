-- +goose Up

-- household_memberships links a login to a household with a household-scoped
-- role. One user can belong to several households (a parent who also helps
-- run a relative's kitchen) and switch between them; see sessions below.
--   owner  - everything, including people, stores, preferences, wipes
--   editor - day-to-day use: plans, shopping, pantry, recipes, items, chat
--   viewer - read-only
CREATE TABLE IF NOT EXISTS household_memberships (
    household_id INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         TEXT    NOT NULL DEFAULT 'editor'
                         CHECK(role IN ('owner', 'editor', 'viewer')),
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (household_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_household_memberships_user ON household_memberships(user_id);

-- The household a session is currently looking at. Per session rather than
-- per user so two devices can sit in different households. NULL = pick the
-- user's first membership.
ALTER TABLE sessions ADD COLUMN active_household_id INTEGER REFERENCES households(id) ON DELETE SET NULL;

-- users.role is now the *instance* role: 'admin' runs the server (settings,
-- AI keys, scraper, accounts), 'member' only uses households they belong to.
-- 'read_only' was never enforced anywhere; it becomes a member whose
-- household role is viewer (backfill below).
UPDATE users SET role = 'member' WHERE role <> 'admin';

-- Pre-multi-tenant installs had one household everyone shared.
INSERT OR IGNORE INTO household_memberships (household_id, user_id, role)
SELECT h.id, u.id, CASE WHEN u.role = 'admin' THEN 'owner' ELSE 'viewer' END
  FROM households h CROSS JOIN users u;

-- +goose Down
UPDATE users SET role = 'read_only' WHERE role = 'member';
ALTER TABLE sessions DROP COLUMN active_household_id;
DROP TABLE IF EXISTS household_memberships;
