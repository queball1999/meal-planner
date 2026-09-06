# Phase 5 Commit Plan — Budget Loop & Outputs
Date: 2026-09-05
Spec ref: §5.2, §5.3, §5.4, §5.6, §7.6

Phase 5 completes the full deliverable set: budget repair loop, recipe detail,
pantry/reuse tracking, feedback capture (👍/👎), leftover/portion planning,
and per-day headcount overrides.

The shopping list (§5.1) is already done (Phase 4). `plan_days`, `meal_recipes`,
`cooked_portions`, `is_leftover`, `leftover_source_meal_id`, and `locked` columns
are all already in the schema — no changes to existing migrations.

---

## Commits

### 1. db: add pantry migration
Files: `db/migrations/00006_pantry.sql`

Table `pantry_items(id, household_id, name, normalized_term, quantity_on_hand,
unit, barcode, updated_at)`. `barcode` supports the Phase 5.8 scan flow but is
not required to be non-empty in Phase 5.

```
db: add pantry items migration
```
Status: ⬜

---

### 2. db: add pantry and plan-day models and store interface
Files: `db/models.go`, `db/store.go`

Models: `PantryItem`, `PlanDay` and their param types
(`CreatePantryItemParams`, `UpdatePantryItemParams`, `UpsertPlanDayParams`).

Store additions:
- `GetMealByID(ctx, mealID) (*Meal, error)` — public wrapper needed for meal-detail handler
- `ListPlanDays(ctx, planID) ([]*PlanDay, error)`
- `UpsertPlanDay(ctx, UpsertPlanDayParams) error`
- `GetPlanDay(ctx, planID int64, date string) (*PlanDay, error)`
- `UpdateMealLeftover(ctx, mealID int64, isLeftover bool, sourceMealID *int64) error`
- `CreatePantryItem(ctx, CreatePantryItemParams) (*PantryItem, error)`
- `ListPantryItems(ctx, householdID int64) ([]*PantryItem, error)`
- `UpdatePantryItem(ctx, UpdatePantryItemParams) error`
- `DeletePantryItem(ctx, id int64) error`
- `MarkShoppingListItemInPantry(ctx, id int64, inPantry bool) error`

```
db: add pantry and plan-day models and store interface
```
Status: ⬜

---

### 3. db: implement pantry and plan-day query methods
Files: `db/pantry.go`, `db/plan_days.go`, `db/meals.go` (add `GetMealByID`)

- `db/pantry.go`: `CreatePantryItem`, `ListPantryItems`, `UpdatePantryItem`,
  `DeletePantryItem`. UNIQUE on `(household_id, normalized_term)` — upsert on conflict.
- `db/plan_days.go`: `ListPlanDays`, `UpsertPlanDay`, `GetPlanDay`.
  `plan_days` uses `INSERT OR REPLACE` on `(plan_id, date)`.
- `db/meals.go`: add public `GetMealByID` (delegates to existing `getMealByID`),
  add `UpdateMealLeftover`.
- `db/shopping_list_items.go`: add `MarkShoppingListItemInPantry`.

```
db: implement pantry and plan-day query methods
```
Status: ⬜

---

### 4. plan: budget repair loop
Files: `plan/repair.go`, `plan/generate.go` (wire in)

`Repair(ctx, store, gen, planID, hh, pricer Pricer, maxIters int) (repaired bool, err error)`:

1. Load plan; if `total_cents <= budget_cents`, return early (nothing to repair).
2. Load all unlocked meals sorted by descending `line_total_cents` from shopping list.
3. Identify the top-N costliest meals (up to 3).
4. Call `gen.Generate` with a repair prompt: "swap these meals with cheaper alternatives
   that honor the same constraints." Use the same `BuildPrompt` envelope but a shorter
   targeted user prompt listing only the meals to replace.
5. Validate the replacement meals; persist them (replace rows in DB).
6. Re-run `pricer` on the updated plan.
7. Repeat up to `maxIters` (default 3, `BUDGET_REPAIR_MAX_ITERS`).
8. If still over budget after all iters, log "budget repair exhausted" — plan is
   marked ready anyway with an honest `confidence_summary`.

Wire: call `Repair` in `plan/generate.go` after the initial `pricer` call, before
`UpdatePlanStatus("ready")`. Skip when `pricer == nil`.

```
plan: add budget repair loop
```
Status: ⬜

---

### 5. plan: leftover/portion planning
Files: `plan/leftovers.go`, `plan/generate.go` (wire in)

`PlanLeftovers(ctx context.Context, store db.Store, planID int64, tolerance bool) error`:

- If `tolerance == false`, return nil immediately.
- Load all meals for the plan, ordered by day+slot.
- For each meal where `cooked_portions > servings`: compute `surplus = cooked_portions - servings`.
  Walk forward in the slot order; for each subsequent slot that is currently not
  already a leftover and whose meal's `servings <= surplus`: mark it as leftover
  (`UpdateMealLeftover`, set `leftover_source_meal_id`), decrement surplus.
- A leftover meal's shopping-list contribution is zeroed (deleted from
  `shopping_list_items` by ingredient refs from `meal_ingredient_refs`). In Phase 5
  this is cosmetic — the items were already written; we flag `in_pantry = true`
  on the leftover meal's shopping list items so they render as "covered".

Wire: call `PlanLeftovers` in `plan/generate.go` after `persistPlan`, before
pricing. Requires `hh.LeftoverTolerance` from the household (or preference row).

```
plan: add leftover/portion planning
```
Status: ⬜

---

### 6. web: meal detail page with recipe steps and feedback
Files: `web/handlers_meals.go`, `web/templates/meal.html`,
`web/routes.go` (add `/meals/{id}`, `/meals/{id}/feedback`, `/meals/{id}/lock`)

`handleMealDetail`: loads `Meal`, `MealRecipe`, `MealIngredient`s, the linked plan's
`WeekStart` for the breadcrumb. Renders steps from `steps_json`, ingredients with
quantity+unit, and 👍/👎 form.

`handleMealFeedback`: `POST /meals/{id}/feedback` — reads `rating` ("1" / "-1"),
calls `store.CreateFeedback`. Redirects back to the meal page.

`handleMealLock`: `POST /meals/{id}/lock` — reads `locked` ("1"/"0"),
calls `store.UpdateMealLocked`. Redirects to `/plan`.

Template `meal.html`: card layout, step list, ingredient list, breadcrumb to plan,
👍/👎 form, lock toggle. If `is_leftover`: show a "♻ Leftovers from [source meal]"
banner instead of the ingredient list.

```
web: add meal detail page with recipe steps and feedback
```
Status: ⬜

---

### 7. web: pantry tracking page
Files: `web/handlers_pantry.go`, `web/templates/pantry.html`,
`web/routes.go` (add `/pantry`, `/pantry`, `/pantry/{id}/delete`, `/pantry/{id}/stock`)

`handlePantryPage`: lists all `pantry_items` for the household.

`handlePantryAdd`: `POST /pantry` — normalizes name, upserts `pantry_items`.

`handlePantryDelete`: `POST /pantry/{id}/delete`.

`handlePantryStock`: `POST /pantry/{id}/stock` — called from the shopping list when
the user checks off an item and moves it to the pantry. Sets `in_pantry = true`
on the `shopping_list_items` row; upserts a `pantry_item` with the bought quantity.

Template: data-table with ingredient name, quantity, unit, last updated; add form at
top; "Remove" per row; empty state with instructions.

```
web: add pantry tracking page
```
Status: ⬜

---

### 8. web: plan calendar — lock, headcount, leftover badges, feedback controls
Files: `web/handlers_plan.go` (extend), `web/templates/plan.html` (extend),
`web/routes.go` (add `/plan/days/{date}/headcount`)

`handlePlanHeadcount`: `POST /plan/days/{date}/headcount` — reads `headcount` int,
calls `store.UpsertPlanDay`. Redirects to `/plan`. (Re-pricing on headcount change
is a stretch; in Phase 5 this just stores the override and shows it.)

Plan template additions:
- Each meal cell: link to `/meals/{id}`, 👍/👎 inline form (CSRF token from hidden
  form, same pattern as shopping list), lock icon (toggles `locked` class).
- Leftover badge: `{{if .IsLeftover}}<span class="badge badge--dim">♻ Leftovers</span>{{end}}`.
- Per-day headcount chip in the day column header: `{{.Headcount}} people` with a
  small `<form>` that POSTs to `/plan/days/{{.Date}}/headcount` to update it.
- Lock icon button per cell: `POST /meals/{id}/lock` with `locked=1/0`.

```
web: plan calendar — lock, feedback, headcount overrides, leftover badges
```
Status: ⬜

---

## Summary

8 commits covering: pantry migration, models, query impls, budget repair loop,
leftover planning, meal detail, pantry page, and plan calendar interactive controls.
All locked columns and leftover fields already exist in the schema — no migration
changes to existing tables needed.
