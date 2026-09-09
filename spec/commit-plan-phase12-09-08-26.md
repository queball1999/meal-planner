# Phase 12 Commit Plan — Background Pricing, Unit System, Generation Tools & Hardening

Date: 2026-09-08
Spec ref: §5.4 (shopping list), §5.6 (leftovers), §6.4 (pricing), §7.4 (generation)
Baseline: `cde7263` (Phase 11 complete + admin events log, tree clean)

Six commits. Three ordering constraints: commit 1 (db pending column) must land
before commit 2 (which wires it into costing.go), commit 3 (preferences db)
before commit 4 (which reads/writes unit_system everywhere), and commit 5
(tool loop) before commit 3 (which wires `runGenerationLoop` into `generate`).
Every other commit is independent. A few files carry hunks for two or three
commits (`web/handlers_shopping.go`, `web/templates/partials/shopping_list_body.html`,
`plan/leftovers.go`, `web/static/css/components.css`); those are hunk-split
and noted per commit.

Legend: ✅ done · ⬜ not started

Exclude from every `git add`:

- `bin/` (build artefact)
- `data/` (SQLite runtime files — WAL, SHM)

---

## Commits

### 1. db: mark shopping-list lines as pending skeleton rows before pricing resolves
Pricing a plan is what takes real time — a live scrape or AI price lookup per
ingredient, then up to three budget-repair rounds that each re-price the whole
list. Before this change the user had to wait for all of that before seeing any
shopping list at all, and the generation progress screen sat on "pricing your
shopping list" with no feedback until the last line resolved.

The seam is a new `pending` boolean on `shopping_list_items`.
`SeedShoppingList` writes every line as `pending=true` immediately after
aggregating ingredients, so the shopping list tab has something to render
(skeleton loading) the instant a plan is marked ready.
`ResolvePricing` turns each pending row into a priced one in place
(`UpdateShoppingListItemPrice`) rather than inserting a duplicate — every other
still-pending row stays a skeleton on the list until its own turn comes up.

A pending row keeps `price_source`/`confidence` at 'estimate' (the CHECK
constraint does not allow a new value without a full table rebuild), so this is
a separate flag rather than a new enum value.

Files: `db/migrations/00027_shopping_list_pending.sql` (new, `pending INTEGER NOT NULL DEFAULT 0`
on `shopping_list_items`), `db/models.go`, `db/shopping_list_items.go` (new
`UpdateShoppingListItemPriceParams` + method), `db/db.go` (test-only fix:
`SetMaxOpenConns(1)` for `:memory:` so background pricing goroutines don't
land on a fresh empty database — see comment in diff).

```
db: mark shopping-list lines as pending skeleton rows before pricing resolves
```
Status: ✅ (`d3f66a1`)

**`UpdateShoppingListItemPrice` touches only price-bearing columns.** It does
not touch `checked`, `in_pantry`, or `pantry_qty_used` — those are edited by
separate handlers and must not be clobbered when a price lands.

**Idempotent re-seed.** `SeedShoppingList` clears any previous list for the
plan first (`DeleteShoppingListItems`), so re-generating a plan that already
has a list starts clean rather than appending duplicates.

**`:memory:` test safety.** `db/db.go` sets `SetMaxOpenConns(1)` when the DSN
is `":memory:"` — each pooled connection to `:memory:` gets its own private
database, and a test's background pricing goroutine would otherwise land on a
fresh connection with no migrations applied. Test-only; real deployments always
pass a file path.

---

### 2. pricing: seed skeleton rows immediately, resolve prices in place
This is the costing-side half of commit 1. `CostPlan` now delegates to two
stages: `SeedShoppingList` (aggregates ingredients, writes pending skeleton
rows) then `ResolvePricing` (prices each row in place). The old single-pass
`CreateShoppingListItem` loop is gone; every priced line calls
`UpdateShoppingListItemPrice` on the row ID that `SeedShoppingList` returned.

The `seededItem` struct carries the aggregate data, the row ID, and the pantry
deduction forward from seeding through resolution, so the same deduction that
was recorded at seed time is applied again when the real price arrives — no
double-counting, no drift.

Files: `pricing/costing.go`, `pricing/costing_test.go`, `pricing/cache.go`
(`sanitizeEstimatePack` called on cached rows too — older estimate rows written
before it existed had the "dozen" + 12 shape).

```
pricing: seed skeleton rows immediately, resolve prices in place
```
Status: ✅ (`5e6cb59`) — **deviations**: (1) `pricing/cache.go`'s hunk (calling
`sanitizeEstimatePack` on cached rows) and the new `costing_test.go` regression
test both depend on `sanitizeEstimatePack`, which only exists once commit 6
lands (`ai_estimate.go`) - staging them here would fail `make check`
immediately. Moved both to commit 6, which already claims `cache.go` in its own
file list. (2) Added the 2-line `web/handlers_shopping.go` hunk (pantry field
passthrough in `handleShoppingItemPriceSet`) that commit 1 needed but its own
file list omitted - without it, `UpdateShoppingListItemPrice`'s now-unconditional
pantry-field write (commit 1) silently zeroes pantry state on every manual
price edit. Commits 4 and 6 both independently note "hunk split with commit 2"
for this file, confirming it belongs here. This commit is `pricing/costing.go`
+ that one hunk of `web/handlers_shopping.go`.

**`ResolvePricing` does not create new rows.** It only updates existing ones.
If a row was deleted between seed and resolve, the update silently affects zero
rows — the caller logs it and moves on, which is the same non-fatal pattern
the old code followed for `CreateShoppingListItem` failures.

**Pantry deduction is tracked per-seed, not re-computed.** The `PantrDeduction`
struct (`Used`, `Covered`) is captured at seed time and replayed at resolve
time, so the total never changes even if pantry stock is edited mid-pricing.

---

### 3. plan: mark plan ready before pricing starts, run pricing in background
The generation flow used to hold up the redirect to `/plan` until pricing and
budget repair finished — the user saw "Generating your plan" with a progress
bar stuck on "pricing your shopping list" for potentially minutes.

Now `generate` marks the plan "ready" as soon as the LLM response is parsed,
validated, and persisted (meals, recipes, leftovers, ingredients). Pricing runs
in a detached goroutine (`priceInBackground`) with its own 15-minute timeout
and fully independent context — by the time this runs, the job that kicked off
generation may already have torn down its own context, and the background work
still has real network calls ahead of it.

When no pricer is configured (tests, or no pricing chain), `EnsureShoppingList`
runs synchronously as a fallback so the plan still has something to shop from.

The progress screen (`plan_generate.html`) drops "Price" and "Shopping list"
from its step bar — those are now handled by the shopping list tab's own
skeleton-loading state and the "Still pricing..." banner.

Files: `plan/generate.go`, `plan/generate_test.go`, `web/handlers_plan.go`
(`resolvePlanForRequest` extracted, `handlePlanListFragment` added, `runPlanGenerationJob`
wired with `PriceChecker`), `web/handlers.go` (`dashPageData.PlanWeekAhead` field;
dashboard follows future-week plan so stats/calendar don't show blank),
`web/templates/index.html` (labels: "Upcoming week", conditional stats strip),
`web/templates/plan_generate.html` (progress bar cleanup; hunk split with commit 6).

```
plan: mark plan ready before pricing starts, run pricing in background
```
Status: ✅ (`60c56ca`) — **deviations**: `web/templates/partials/shopping_list_body.html`
and `web/shopping_list_render_test.go` do **not** land here despite carrying
the `Pending`/`Pricing` skeleton markup this commit's Go side introduces -
their diffs are inseparably interleaved with commit 6's `MealRef`/conv-popup
markup at the template-conditional level (nested `{{if}}` blocks alternating
concerns line by line), and `shopping_list_render_test.go` renders and asserts
on both in the same test. Splitting by hand risked a broken template with no
compiler to catch it. Both move wholly to commit 6; the gap is a purely
cosmetic one (skeleton rows render as normal priced rows until commit 6
lands), nothing depends on it functionally or in tests. `web/templates/
plan_generate.html`'s progress-bar cleanup also lands here in full - no
commit-6-specific content was found in its diff despite the spec's "hunk split
with commit 3" note on the commit-6 side.

**`priceInBackground` uses `context.Background()`, not the generation ctx.**
A deadline blown during pricing must not strand the plan in "generating" — the
meals are already persisted, and the status update above uses a short detached
timeout for exactly that reason.

**Budget repair runs after pricing, also in background.** The old flow ran
`Repair` inline; now it runs in the same goroutine but with `nil` for the
progress job (the SSE stream already closed once the plan was marked ready).

**Dashboard "plan week ahead".** When the latest plan is for a week other than
the one containing today (generation plans the next full week once this one is
underway), the dashboard follows that plan — `PlanWeekAhead` flips the stats
strip label ("This week spent" → "Spent"), the calendar widget's reference date,
and the week heading ("This week" / "Week of" → "Upcoming week"). Without this,
the live-week view would show blank stats while the card shows the plan.

---

### 4. db + web: store household unit system preference, apply on display
A household picks one measurement system for shopping-list display: "as-is"
(default, leaves every quantity in its stored unit), "metric" (re-expresses
weight/volume into g/kg, ml/l), or "imperial" (oz/lb, tsp/tbsp/fl-oz/cup/pint/
quart/gallon). Countable units (each, can, bunch, clove, …) are never converted.

The migration adds `unit_system TEXT NOT NULL DEFAULT 'as-is'` to `preferences`.
`GetPreferences` / `UpsertPreferences` read/write the column; empty strings
default to "as-is" both ways.

On the web side, `buildShoppingListView` reads the household's unit system and
passes it through `displayQtyUnit` (from `pricing/units_display.go`) to each
line's buy label before rendering. CSV export gets the same treatment so a
download matches what the user sees on screen.

**4b. Reusable unit picker partial.** The `unitSelect` dropdown replaces bare
`<input type="text">` fields wherever a unit is edited — pantry add dialog,
item edit dialog, price editor. It renders a `<select>` populated from
`commonUnits` (each, mg, g, kg, oz, lb, tsp, tbsp, fl-oz, cup, pint, quart,
gallon, ml, l), keeping an item's own unusual stock unit in the list (and first)
via `unitOptionsFor`. The partial accepts `name`, `id`, and `current` values.

Files: `db/migrations/00028_preferences_unit_system.sql` (new),
`db/models.go`, `db/preferences.go`, `web/handlers_shopping.go` (displayQtyUnit
calls, CSV export unit treatment; hunk split with commits 2 and 6),
`web/templates/preferences.html`, `web/templates/partials/unit_select.html`
(new — dropdown for as-is/metric/imperial with icons; also used by commit 6),
`web/templates/items.html` (unit input → unitSelect partial; hunk split with
commit 4b), `web/templates/pantry.html` (unit input → unitSelect partial;
hunk split with commit 4b), `web/static/css/components.css` (dropdown styling;
hunk split with commit 6).

```
db + web: store household unit system preference, apply on display
```
Status: ✅ (`c40b6fc`) — **deviations**: (1) also includes `web/format.go`
(`displayQtyUnit`/`displayQtyLabel`) and `pricing/units_display.go` +
`units_display_test.go` (`DisplayQuantity`) - the spec's prose names these but
its file list omitted them; without them nothing in this commit compiles. (2)
also includes `web/handlers_preferences.go` (reads/writes `UnitSystem` on the
preferences form) - same omission. (3) `web/handlers_shopping.go`'s on-screen
shopping-list line (`buildLineItem`) does **not** get unit-system display here:
its signature line inseparably combines the `unitSystem` param with the
`mealTitleByIngredient map[int64]string → map[int64]db.MealRef` change, which
only compiles once `db.MealRef` exists (commit 6). That whole slice - the
`prefs`/`unitSystem` lookup in `buildShoppingListView`, the signature, the
`displayQtyUnit` call in `buildLineItem`'s body, and the priceSet call site -
moves to commit 6. This commit's `handlers_shopping.go` contribution is the CSV
export path only (self-contained, no `MealRef` dependency).

**`displayQtyUnit` promotes to the next unit up once the number would otherwise
read awkwardly large.** 1200 g → "1.2 kg", 900 g stays "900 g"; 20 oz → "1.25
lb", 12 oz stays "12 oz". The threshold is `minReadable = 1.0`.

---

### 5. plan: add generation tool loop with pantry, recipe search, item search & price check
The model used to generate ingredients blind — no way to look up what the
household already has, no way to check real prices before committing to a plan,
and no way to reuse existing recipes instead of inventing near-duplicates.
This let a plan quietly come in far over budget with every ingredient looking
individually reasonable.

`runGenerationLoop` wraps the model call in a tool-use loop (up to 24 steps).
The model replies with either a tool call JSON object or the final plan JSON.
Four tools are available:

- `read_pantry()` — list household pantry items (name, quantity, unit)
- `search_recipes(query?)` — search saved catalog recipes by title/tag; reuse
  a good match instead of inventing a near-duplicate
- `search_items(query)` — find canonical catalog names for grocery terms
- `check_price(ingredient)` — look up a real price for one ingredient

The tool protocol mirrors `agent/loop.go`'s `protocolPrompt` shape (same "tool"
vs. final-answer JSON convention, for cross-provider portability) but ends in
the plan schema rather than a free-text "say". Tool descriptions are hand-written
(short readable list, not JSON Schema) because models follow them more reliably
and it costs a fraction of the tokens.

Files: `plan/tools.go` (new), `plan/toolloop.go` (new),
`plan/toolloop_test.go` (new), `plan/prompt.go` (append preamble + tool
descriptions to system prompt), `plan/generate.go` (wire `runGenerationLoop`
into `generate`, pass `PriceChecker` through `Generate`/`GenerateForWeek`).

```
plan: add generation tool loop with pantry, recipe search, item search & price check
```
Status: ✅ (`858af5d`) — **deviations**: `plan/prompt.go`'s actual diff turned
out to be entirely the leftovers first-day-guard wording (commit 6b), not a
tool-loop preamble - moved there. `web/handlers_plan.go` also needed
`buildPriceChecker` and `checker` threaded through `runPlanGenerationJob`/
`startPlanGeneration(ForWeek)` (omitted from this commit's file list, but
required for the new `Generate`/`GenerateForWeek` signature to have a caller).
Test files not in the spec's list also needed the same one-argument fix
(`plan/persist_recipes_test.go`, part of `plan/shopping_guarantee_test.go`) -
included here; `homeassistant/sync_test.go` and a couple of pure-whitespace
hunks elsewhere are unrelated pre-existing changes, left alone.

**`genToolCtx` is scoped to generation only.** It duplicates the household-scoped
read access that the chat assistant's tools use, rather than importing `agent`
(backwards import cycle: agent already imports plan via fill_slot/move_meal).

**24 steps is generous but bounded.** Enough to check pantry, a couple recipe
searches, and a price on every ingredient in a substantial week (30-40 lines)
without letting a confused model loop indefinitely — each step is a full model
round-trip, so this also bounds how long generation can take before giving up.

**`looksLikeToolCall` tries before assuming final plan.** A stray tool call must
never be handed to `json.Unmarshal(&GeneratedPlan{})` and silently produce a
zero-meal plan.

---

### 6. pricing + web + plan: harden bad LLM replies, fix leftovers, add conversions popup & scrape debug
Six small hardening changes in one commit because each is independently narrow
and none depends on the others.

**6a. `sanitizeEstimatePack`** fixes the single most damaging shape of a bad LLM
price reply: a compound purchase_unit ("dozen") paired with a pack_size that is
the unit's own expansion factor (12) rather than how many of that unit are in
one pack (a carton is 1 dozen, an 18-count is 1.5). Left through, "dozen" + 12
reconciles to 144 eggs a pack — one carton then reads as "144 eggs, $3.99" and
every downstream pack-count divides by 144. Only whole multiples of the factor
are pulled back (12 → 1, 24 → 2); an oddball like 18 is left alone.

**6b. Leftovers first-day guard.** The week's first day cooks nothing else to
have leftovers from. Guards against the model titling a first-day meal as
leftovers anyway (BuildPrompt tells it not to; this is the backstop).

**6c. Quick conversions popup.** A ruler/swap icon next to each shopping-list
line opens a floating popup showing the quantity expressed in every other unit
of its family (weight lines offer g/kg/oz/lb; volume lines offer tsp/tbsp/fl-oz/
cup/ml/l/pint/quart/gallon). Dual-mode: hover previews, click pins open. One
floating popup for the whole list, moved to whichever button is active.

**6d. Scrape debug log.** An in-memory ring buffer captures every `FetchSmart`
call (URL, status code, bytes, challenge detection, duration) so the admin
Audit Log can show why a page came back empty alongside the LLM call log.

**6e. Meal tag hovercards.** Each meal pill on a shopping-list line carries the
meal's own ID (`data-meal-id`) so hovercard.js can fetch and display a recipe
preview on hover — same dual-mode pattern as the conversions popup.

**6f. Plan generate UI cleanup.** Progress bar drops "Price" and "Shopping list"
steps (those now run in background after the plan is ready). Subtitle explains
pricing happens asynchronously. Comment documents why those steps were removed.

Files: `pricing/ai_estimate.go`, `pricing/ai_estimate_test.go`, `pricing/cache.go`
(`sanitizeEstimatePack` called on cached rows too),
`plan/leftovers.go`, `plan/leftovers_test.go`, `web/static/js/conversions.js`
(new — quick unit-conversion popup), `scrape/debug.go` (new — in-memory ring
buffer for scrape fetches), `scrape/fetch_smart.go` (record to debug log),
`db/meal_ingredients.go` (`MealRef` type; `ListMealTitlesByIngredientID` now
returns meal id + title instead of just title), `db/store.go` (interface line
for `MealRef` return type change),
`web/handlers_shopping.go` (quickConversions endpoint, `mealTagsFor` refactor
to use `MealRef`; hunk split with commits 2 and 4),
`web/templates/partials/shopping_list_body.html` (conversions popup buttons +
pending skeleton rows + meal hover badges; hunk split with commits 2 and 4),
`web/static/css/components.css` (pending item styling, conversions popup styles;
hunk split with commit 4),
`web/templates/plan_generate.html` (progress bar cleanup; hunk split with
commit 3),
`web/render.go` (`renderFragment` for polling fragments; `unitOptions`/`unitOptionsFor`
template funcs), `web/routes.go` (`/list/{id}/conversions` + `/plan/list/fragment`
routes),
`web/shopping_list_render_test.go` (pending row rendering test, `MealRef`-aware
tag tests),
`web/templates/items.html` (data-table__grow on name column),
`web/templates/pantry.html` (data-table__grow on ingredient column),
`web/static/css/layout.css` (.data-table__grow CSS rule),
`web/templates/llm_log.html` (audit log: LLM + scrape merged, kind/status filters;
also uses renderFragment if applicable),
`web/handlers_settings.go` (audit log merge: LLM + scrape entries sorted by time,
kind/status filters),
`cmd/build_catalog/main.go` (-enrich-only default → true, doc update),
`catalog/seed_items.json` (re-curated: removed vague photo-caption items like
"bowl of tagliatelle", added real produce with conversions).

```

```
pricing + web + plan: harden bad LLM replies, fix leftovers, add conversions popup & scrape debug
```
Status: ✅ (`6850a1d`) — includes the deferred-from-commit-3/4 hunks noted
above, plus `web/handlers_instore.go`, `web/icons.go`,
`web/templates/admin_prices.html`, `web/templates/item_detail.html` (unit-system
rollout finishing up, all omitted from commit 4's file list). Left unstaged and
uncommitted, in all six commits, as genuinely unrelated pre-existing changes:
`homeassistant/sync_test.go` (gofmt-only), `pricing/convert.go` /
`pricing/quantity.go` (gofmt-only), one hunk of `plan/generate_test.go`
(gofmt-only), and two font-size tweaks in `web/static/css/components.css`
(`.plan-col__day`, `.meal-card__slot` - unrelated to this plan).

**`sanitizeEstimatePack` is a safety net, not a correction.** It only touches
pack sizes that are exact whole multiples of the unit's expansion factor. An
LLM reply of "dozen" + 18 (a real 18-count carton) passes through untouched.

**Leftovers guard uses first-seen day, not calendar logic.** The meals are
already sorted by slot order at this point; the first entry in that sorted list
is the week's first day. No date arithmetic needed.

**Conversions popup shares hovercard.js's floating-panel pattern.** One DOM
element, moved to the active button at show time, `position: fixed` to avoid
scroll-clip issues.

**Audit log merge.** LLM calls and scrape fetches now appear together on the
audit log page, sorted newest-first across both sources. Kind/status filters
limit which entries are included. Uses `scrape.GlobalDebugLog.Entries()` from
the new debug log.

**`MealRef` type change.** `ListMealTitlesByIngredientID` now returns `{MealID,
Title}` instead of just title, so meal pills carry the real meal id for
hovercard.js. Callers (`web/handlers_shopping.go`, `web/shopping_list_render_test.go`)
updated accordingly.

**Catalog re-curation.** `seed_items.json` removed vague photo-caption items
("bowl of plain cooked tagliatelle pasta", "crusty loaf of white bread") and
added real produce with conversions (red onion 1 each = 110 g, sweet potato
1 each = 130 g, cauliflower 1 head = 580 g). `build_catalog` defaults to
`-enrich-only=true` so unmatched photos are dropped rather than appended as
new items.

---

## Suggested order

```
1  db: mark shopping-list lines as pending skeleton rows before pricing resolves
2  pricing: seed skeleton rows immediately, resolve prices in place              (needs 1)
3  plan: mark plan ready before pricing starts, run pricing in background        (needs 1, 2, 5)
4  db + web: store household unit system preference, apply on display
5  plan: add generation tool loop with pantry, recipe search, item search & price check
6  pricing + web + plan: harden bad LLM replies, fix leftovers, add conversions popup & scrape debug
```

`make check` should pass after every commit; the only hard ordering constraints
are 1 → 2 → 3 and 5 → 3. Commit 4 is independent of the pricing chain but must
land before any file that reads `UnitSystem`. Commit 6 has hunk splits across
files shared with commits 2, 3, and 4.
