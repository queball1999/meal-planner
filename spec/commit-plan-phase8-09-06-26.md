# Phase 8 Commit Plan — Items Catalog, HA Sync, Plan Lifecycle & Fixes

Date: 2026-09-06
Spec ref: §6.3 (items), §11 (Home Assistant), §5.5/§5.7 (plan lifecycle), §8 (scraping)

This plan covers all uncommitted work since the last commit (fc89b15).
88 files changed, +5,896 / -1,259 lines (per `git status`, before the two
housekeeping items below are applied).

The work falls into **13 logical commits** plus 2 housekeeping steps,
each self-contained and reviewable in isolation.

---

## Housekeeping (do first)

### 0a. Add `*.exe` to `.gitignore` and drop the stray binary
`probe_llm.exe` is a compiled Windows binary sitting at the repo root
(build output of `cmd/probe_llm`). It must not be committed.

Files: `.gitignore` (extend), remove `probe_llm.exe` from the working tree
(not tracked, so no `git rm` needed — just don't stage it / delete it locally).

```
chore: ignore compiled binaries
```
Status: ✅

---

## Commits

### 1. db: add items catalog schema and derived unit conversions
Files: `db/migrations/00010_items.sql` (new), `db/migrations/00013_derived_conversions.sql` (new),
`db/items.go` (new), `db/item_conversions.go` (new), `db/item_packages.go` (new),
`db/models.go` (Item, UnitConversion, ItemStorePackage, PriceHistoryEntry, ItemFilter,
CreateItemParams/UpdateItemParams hunks), `db/store.go` (Item*/Conversion*/Package* interface methods)

**Items table** (`00010_items.sql`): household's canonical grocery-item catalog —
name, normalized_term, category, stock_unit, default_purchase_qty, photo metadata.
`meal_ingredients`, `pantry_items`, `shopping_list_items`, `price_cache`, and
`manual_prices` gain an `item_id` FK.

**Derived conversions** (`00013_derived_conversions.sql`): `derived` flag on
`unit_conversions` marking machine-generated one-hop edges to an item's stock
unit, so costing never has to BFS the conversion graph at request time.

**DB layer** (`db/items.go`, `db/item_conversions.go`, `db/item_packages.go`):
CRUD for items, hand-entered/derived conversions, and per-store package sizes.

```
db: add items catalog schema, conversions, and store packages
```
Status: ✅

---

### 2. catalog+pricing: link free-text ingredients to items and auto-derive conversions
Files: `catalog/catalog.go` (new), `catalog/conversions.go` (new), `catalog/seed.go` (new),
`catalog/seed_items.json` (new), `catalog/catalog_test.go` (new),
`pricing/convert.go` (new), `pricing/convert_test.go` (new),
`pricing/autoconv.go` (new), `pricing/autoconv_test.go` (new),
`cmd/build_catalog/main.go` (new),
`db/meal_ingredients.go` (`SetMealIngredientItem`, `ItemID` column),
`db/pantry.go` (`SetPantryItemItem`), `main.go` (catalog seed/backfill on boot)

**Catalog package**: `EnsureItem` normalizes a free-text ingredient name and
links it to (or creates) the household's canonical item row.

**Pricing conversions** (`pricing/convert.go`, `pricing/autoconv.go`):
`AutoConversions` recomputes derived one-hop unit-conversion rows whenever an
item's stock unit, default buy qty, or hand-entered conversions change.

**`cmd/build_catalog`**: offline tool to (re)generate `seed_items.json` from a
reference dataset.

```
catalog: link ingredients to items and auto-derive unit conversions
```
Status: ✅

---

### 3. items: image handling, picker UI, and item detail/list pages
Files: `items/images.go` (new), `web/handlers_items.go` (new), `web/items_render_test.go` (new),
`web/templates/items.html` (new), `web/templates/item_detail.html` (new),
`web/static/img/items/placeholder.svg` (new),
`web/static/css/choices.min.css` (new, vendored), `web/static/css/choices-overrides.css` (new),
`web/static/js/choices.min.js` (new, vendored),
`web/routes.go` (`/pantry/items*`, `/item-images/{name}`),
`web/templates/pantry.html` (pantryTabs partial + item picker wiring),
`web/templates/partials/pantry_tabs.html` (new),
`config/config.go` + `settings/registry.go` (`ITEM_IMAGE_DIR`)

**Choices.js**: vendored autocomplete/multi-select library powering the item
picker (pantry, recipe ingredient linking).

**Item pages**: list + detail views showing category, stock unit, price
history, and conversions for one catalog item.

```
web: add item catalog pages, image handling, and autocomplete picker
```
Status: ✅

---

### 4. plan: ingredient estimated price fallback
Files: `db/migrations/00015_ingredient_est_price.sql` (new),
`db/meal_ingredients.go` (`EstPriceCents` column, hunks not covered by commit 2),
`db/models.go` (`MealIngredient.EstPriceCents`),
`plan/prompt.go`, `plan/generate.go`, `pricing/costing.go`, `pricing/ai_estimate.go`,
`pricing/quantity.go`, `plan/generate_test.go` (new), `pricing/costing_test.go` (new)

The meal-planning LLM now returns a rough US grocery price per ingredient at
generation time. The pricing chain falls back to that number instead of $0
when every live provider (scrape/AI estimate) comes up empty.

```
plan: fall back to LLM-estimated ingredient price when pricing fails
```
Status: ✅

---

### 5. plan: soft-cancel on regenerate, and archived plan history
Files: `db/migrations/00016_plan_canceled.sql` (new),
`db/plans.go` (`DeletePlan`, `DeleteAllPlansForHousehold`, `CancelOtherPlansForWeek`,
`canceled` column in scans), `db/models.go` (`Plan.Canceled`),
`db/plans_test.go` (new), `plan/generate.go` (`CancelOtherPlansForWeek` call),
`plan/job.go`, `web/templates/index.html` + `web/templates/plan.html`
(canceled badge, regenerate confirm dialog, generating/error banners),
`web/handlers_plan.go` + `web/handlers_plan_test.go` (new) (plan delete handler)

Regenerating a plan for a week that already has one no longer deletes the old
plan — it's flagged `canceled` and kept in `/plan/history`. `GetLatestPlan` /
`GetPlanByWeekStart` skip canceled rows. A separate per-plan delete action
(history table) permanently removes one archived plan.

```
plan: soft-cancel regenerated plans instead of deleting them
```
Status: ✅

---

### 6. plan: per-day servings scaling
Files: `db/migrations/00017_servings_scaling.sql` (new),
`db/plan_days.go` (`ScaleMealsForDay`), `db/scale_day_test.go` (new),
`db/models.go` (`Meal.BaseServings`/`BaseCookedPortions`, `MealIngredient.BaseQuantity`,
`ScaleDayResult`), `db/meals.go`, `db/meal_ingredients.go` (base-column writes),
`web/server.go` (`repriceMu`/`repricingPlans` — serializes concurrent rescale +
shopping-list rebuild), `web/templates/plan.html` (headcount form),
`web/templates/meal.html` (scaled-from-N banner), `web/handlers_plan.go`

Changing a day's headcount rescales servings, cooked portions, and every
ingredient quantity from the as-generated base values (not the current ones),
so 2 → 6 → 3 people always lands on the same numbers as going straight to 3.

```
plan: add per-day headcount rescaling from base servings
```
Status: ✅

---

### 7. db+web: Home Assistant shopping-list sync
Files: `cryptbox/cryptbox.go` (new), `cryptbox/cryptbox_test.go` (new),
`db/secrets.go` (new), `db/migrations/00011_secrets.sql` (new),
`db/ha_sync.go` (new), `db/migrations/00012_ha_sync.sql` (new),
`homeassistant/client.go`, `homeassistant/errors.go`, `homeassistant/scheduler.go`,
`homeassistant/sync.go`, `homeassistant/sync_test.go` (all new),
`settings/ha.go` (new), `web/handlers_ha.go` (new),
`web/templates/partials/ha_setup_dialog.html` (new),
`db/store.go` (secrets + HASyncRow methods),
`config/config.go` + `settings/registry.go`
(`HA_BASE_URL`, `HA_TOKEN`, `HA_TODO_ENTITY`, `HA_SYNC_INTERVAL_MINUTES`, `HA_ITEM_FORMAT`),
`web/server.go` (`haScheduler`, `box *cryptbox.Box`),
`main.go` (`cryptbox.New`, `go srv.RunHAScheduler`),
`web/routes.go` (`/settings/ha/*`, `/list/sync*`),
`web/icons.go` (`sync`, `home-assistant` icons),
`db/db.go` (`busy_timeout=10000` pragma — HA scheduler can race costing writes)

**Secrets** (`cryptbox`, `db/secrets.go`): authenticated encryption at rest
for the HA long-lived access token, keyed off `SESSION_SECRET`.

**Sync** (`homeassistant/`): pushes the shopping list to a Home Assistant
to-do entity and pulls back checked-off state, mapped by
`(household, normalized_term)` so the mapping survives weekly plan rebuilds.

```
web: add Home Assistant shopping-list sync with encrypted token storage
```
Status: ✅

---

### 8. web: shopping list partial refactor and price editor
Files: `web/templates/partials/shopping_list_body.html` (new, replaces deleted
`web/templates/shopping_list.html`), `web/shopping_list_render_test.go` (new),
`plan/shopping_guarantee_test.go` (new), `db/shopping_list_items.go`,
`web/handlers_shopping.go`, `web/render.go` (`renderWithPage`/pageSlug split),
`web/routes.go` (`/list/export`, `/list/{id}/price`, `/list/ai-cost`),
`web/templates/plan.html` (subtabs + `{{template "shoppingListBody"}}`),
`db/migrations/00014_price_history.sql` (new)

The shopping list moves from its own page template into a partial shared
between `/plan` and `/plan/list`, with subtab navigation. The pencil-icon
price editor (shopping list + item/admin price forms) now logs every
operator price write to `price_history` so a price's movement over time can
be shown.

```
web: refactor shopping list into a shared partial with price history
```
Status: ✅

---

### 9. web: dashboard calendar widget
Files: `web/templates/index.html` (`cal-card`/`cal-grid`/`cal-toolbar` block),
`db/meals.go` (`ListMealsByHouseholdRange`), `db/store.go` (interface entry),
`web/handlers.go` (calendar data builder), `web/static/css/components.css` (calendar styles)

Adds a month-view calendar to the dashboard showing planned meals across
weeks, independent of the current plan's day list.

```
web: add dashboard calendar widget
```
Status: ✅

---

### 10. web: settings danger zone (permanent data wipe)
Files: `web/handlers_danger.go` (new), `db/danger_wipe_test.go` (new),
`web/routes.go` (`/settings/danger/{target}`),
`db/pantry.go`, `db/plans.go`, `db/catalog_recipes.go`, `db/shopping_list_items.go`
(`DeleteAll*ForHousehold` methods), `web/templates/settings.html` (danger zone section)

Settings → Danger zone: permanently delete one category of household data
(plans, pantry, recipes, shopping list), each confirmed client-side before
the request is sent. Unlike a regenerate's soft-cancel (commit 5), nothing
here is kept for history.

```
web: add settings danger zone for permanent data wipes
```
Status: ✅

---

### 11. web: About page and auto-plan scheduler status
Files: `web/handlers_about.go` (new), `web/handlers_about_test.go` (new),
`web/scheduler_plan.go` (new), `web/scheduler_plan_test.go` (new),
`web/templates/about.html` (new), `web/templates/attributions.html` (new),
`web/routes.go` (`/about`, `/attributions`),
`web/server.go` (`startedAt`, `autoPlanMu`/`autoPlanCheckedAt`),
`config/config.go` + `settings/registry.go` (`AUTO_PLAN_HOUR`),
`main.go` (`go srv.RunAutoPlanScheduler`)

**Auto-plan scheduler** (`web/scheduler_plan.go`): ticks every 15 minutes and,
once `AutoPlanHour` is configured, triggers plan generation the night before
the week starts.

**About page**: build info, uptime, and a background-process table (auto-plan
scheduler last-tick time, HA scheduler) so an operator doesn't have to read
`main.go` to find out what's running.

```
web: add auto-plan scheduler and About page
```
Status: ✅

---

### 12. llm: reasoning-model compatibility for OpenAI-compatible providers
Files: `llm/openai.go`, `llm/openai_test.go` (new), `llm/generator.go` (`SuppressReasoning`),
`llm/freetext.go`

Suppresses `enable_thinking` and strips `<think>` blocks for reasoning models
(e.g. Qwen) served through OpenAI-compatible endpoints; adds diagnostics for
empty-content responses via `reasoning_content`/`finish_reason`.

```
llm: handle reasoning-model output on OpenAI-compatible providers
```
Status: ✅

---

### 13. scrape: fix Browserless userAgent shape and detect empty-shell loads
Files: `scrape/render.go`, `scrape/fetch_smart.go`, `scrape/storecontext.go`,
`cmd/probe_clearance/main.go` (new), `cmd/probe_llm/main.go` (new),
`spec/findings_090606.md` → `spec/findings_090626.md` (new)

**Bug fix**: `fetchViaBrowserless` sent `userAgent` as a bare string; current
Browserless builds require an object, so the FlareSolverr→Browserless
clearance handoff failed 100% of the time on every store. Fixed to send the
object shape.

**Empty-shell detection**: a store-context fetch that returns HTTP 200 with
no products rendered (`contentReady` false) now falls through to the
clearance handoff instead of being reported as a success.

```
scrape: fix clearance handoff userAgent shape and detect empty-shell loads
```
Status: ✅

---

### 14. misc: recipe step-number stripping and template helpers
Files: `web/render.go` (`stepText`/`stripStepNumber`, `titleCase`, `slots` helpers),
`web/templates/recipe.html`, `web/templates/meal.html` (remaining hunks not
covered by commits 6/8), `web/static/css/tokens.css`, `web/static/css/wizard.css`,
`web/static/js/main.js` (remaining hunks), `docker-compose.yml`, `Dockerfile`,
`.env.example` (remaining env vars not covered above)

Strips baked-in step numbers ("1. Preheat oven...") from scraped/imported
recipe instructions so the template's own numbered `<ol>` doesn't double up.

```
web: strip redundant step numbers from imported recipe instructions
```
Status: ✅

---

### 15. docs: update phase 7 commit plan and add scraping findings doc
Files: `spec/commit-plan-phase7-09-06-26.md` (mark complete — already done in
last commit but has trailing edits), `spec/findings_090626.md` (new, if not
folded into commit 13)

```
docs: record phase 7 completion and bot-detection probe findings
```
Status: ✅

---

## Summary

15 commits + 1 housekeeping step:

0. ✅ **Ignore binaries** — `.gitignore` + drop `probe_llm.exe`
1. ✅ **Items schema** — items/conversions/packages tables and DB layer
2. ✅ **Catalog linking** — free-text → item linking, auto-derived conversions
3. ✅ **Item pages + picker** — image handling, Choices.js, item list/detail
4. ✅ **Est-price fallback** — LLM-supplied ingredient price as last resort
5. ✅ **Plan soft-cancel** — regenerate keeps history instead of deleting
6. ✅ **Servings scaling** — per-day headcount rescale from base values
7. ✅ **HA sync** — encrypted token storage + shopping-list sync
8. ✅ **Shopping list refactor** — shared partial + price history
9. ✅ **Dashboard calendar** — month-view widget
10. ✅ **Danger zone** — permanent per-category data wipe
11. ✅ **About page + auto-plan scheduler**
12. ✅ **LLM reasoning-model compat**
13. ✅ **Scrape fixes** — userAgent shape bug, empty-shell detection
14. ✅ **Misc** — step-number stripping, template helpers, config cleanup
15. ✅ **Docs** — phase 7 wrap-up + findings doc

All Phase 8 commits complete. Working tree is clean except this plan doc
itself (committed separately once finalized).

**Note on compilability**: several shared files (`db/models.go`, `db/store.go`,
`web/routes.go`, `web/server.go`, `main.go`, `config/config.go`,
`settings/registry.go`, `.env.example`) carry hunks for more than one feature
and were committed whole at a single commit rather than split hunk-by-hunk, so
not every individual commit in this history necessarily builds in isolation —
only the final state (`git log -1`) is guaranteed to. Treat this history as
organized-by-feature for review purposes, not as a bisectable sequence.

**Before committing**, verify boundaries on the shared files
(`db/models.go`, `db/store.go`, `web/routes.go`, `web/server.go`,
`config/config.go`, `settings/registry.go`, `main.go`) with
`git add -p` — several of them carry hunks for more than one commit above
and were assigned to their primary/first-introduced feature; splitting by
hunk during staging is more reliable than by file.
