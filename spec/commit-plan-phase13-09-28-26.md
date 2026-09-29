# Phase 13 — Multi-tenant households + RBAC (2026-09-28)

Goal: one Go Eat instance hosts several households whose data never crosses,
and each person's rights inside a household are enforced server-side.

The schema was built for this (§10.1: "multi-tenant is a later migration, not
a reshape") - almost every table already carries `household_id`. What was
missing was (a) a user↔household link, (b) any role enforcement at all (the
`read_only` role existed but nothing checked it), and (c) ownership checks on
the ~40 handlers that load a row by bare id from the URL.

## Role model

Two independent axes:

| Axis | Values | Governs |
|---|---|---|
| Instance role (`users.role`) | `admin`, `member` | Settings page, AI provider keys, scraper tooling, AI pricing references, LLM logs, audit log, creating households + user accounts. An instance admin is also an implicit **owner** of every household. |
| Household role (`household_memberships.role`) | `owner` > `editor` > `viewer` | viewer: read everything in the household. editor: plan, shop, pantry, recipes, items, chat. owner: preferences, people, stores, manual prices, danger-zone wipes, managing memberships. |

The active household lives on the **session** (`sessions.active_household_id`),
so two browser sessions of one person can sit in different households.

## Commits

1. **db: household memberships, active household on session** — migration
   00032 (`household_memberships`, `sessions.active_household_id`,
   `read_only`→`member`, backfill every existing user into every existing
   household: admin→owner, others→viewer). Store methods for memberships,
   household list/rename/delete, user list/role/delete, and
   `HouseholdOwns(ctx, hh, kind, id)` - the single ownership primitive.
   `GetHousehold(ctx)` becomes `GetHousehold(ctx, id)` so the compiler finds
   every "the one household" assumption.
   Status: done

2. **middleware: resolve active household + role gates** — LoadSession picks
   the session's active household if the user may see it, else their first
   membership. `RequireRole(viewer|editor|owner)`, `RequireInstanceAdmin`.
   `SetupDone` flag replaces "household exists" as the first-run test.
   Status: done

3. **web: route-level RBAC + ownership checks on every by-id handler** — every
   route in routes.go now names its minimum role. Every handler taking an id
   from the path/form checks `HouseholdOwns` and answers 404 on a miss (not
   403 - don't confirm other households' ids exist).
   Status: done

4. **web: households page** — list/switch/create households, add/remove
   people and change their household role, admin user management.
   Status: done

5. **schedulers: iterate all households** — auto-plan and image backfill loop
   every household; HA sync binds to the household that saved the HA config
   (`HA_HOUSEHOLD_ID`).
   Status: done

## Cross-tenant bugs found by the audit (all fixed, most covered by tests)

Most could only bite once there were two households, but they had to be fixed
before there could be:

- Meal detail / feedback / lock, pantry delete / stock, recipe detail / edit /
  delete / reimport / image, shopping-list check / have, store delete, manual
  price delete: loaded or wrote a row by bare id, no household check.
- Manual price create, item package upsert, item "preferred store", shopping
  price-set: trusted a `store_id` from the form.
- `/pantry/items/{id}/packages/{pkgID}/delete` and `.../conversions/{cid}/delete`
  checked `{id}` but deleted any child id (QSS §17). The conversion one could
  delete *global* unit conversions from any item. Now scoped in SQL
  (`WHERE id = ? AND item_id = ?`, `ErrNotFound` on zero rows).
- Barcode scan returned a pinned product from any household's stores.
- No role was ever enforced: `read_only` existed only as a label.

6. **ui: hide what the role can't use** — `<body data-role data-admin>` plus
   one CSS rule: viewers lose every POST form (bar `data-viewer-ok`: sign out,
   switch household) and anything `data-requires`; editors lose
   `data-requires="own"`. Plan + shopping list reuse their existing read-only
   mode for viewers; Preferences renders as a disabled fieldset for
   non-owners; the AI chat is only offered to editors and up.
   Status: done

7. **security: filippo.io/csrf, setup token, scoped images** — CSRF moves to
   `filippo.io/csrf/gorilla` (QSS §6, GO-2025-3884) with a scheme-qualified
   `TrustedOrigins` and a scheme guard. The setup wizard needs a token
   (`SETUP_TOKEN`, or a random one printed once to stderr; per-IP rate limit,
   constant-time compare, dies on claim, fails closed; QSS §15). Recipe/item
   photos serve only when a row in the active household references the file.
   Status: done

## Known gaps, deliberately left

- The AI chat's pending confirmation is keyed by household, so any editor in
  it can approve another editor's proposed change.
- `middleware.ClientIP` trusts `X-Forwarded-For` from any peer (QSS §5.4), so
  the setup-token rate limit can be dodged by rotating that header. The
  128-bit token keeps guessing infeasible regardless.
