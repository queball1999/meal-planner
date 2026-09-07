# Phase 9 Commit Plan — UI Platform, Item Linking, Plan Lifecycle & AI Chat

Date: 2026-09-06
Spec ref: §4 (UI), §5.5/§5.7 (plan lifecycle), §6.3 (items), §7 (shopping list), §9 (AI)
Baseline: `d916d02` (Phase 8 complete, tree clean)

Phase 9 turns the backlog into **17 commits**, ordered so shared UI
primitives land before the features that depend on them.

Legend: ✅ done · 🔨 in progress · ⬜ not started

---

## Commits

### 1. web: custom dialog/modal and tooltip primitives
Replaces the browser's `confirm()` everywhere with an in-app dialog, ported
from `slack-llm-proxy` (`web/static/js/modal.js`, `tooltip.js`,
`partials/modal_open.html` / `modal_close.html`, `web/MODALS.md`).

Files: `web/static/js/modal.js` (new), `web/static/js/tooltip.js` (new),
`web/static/css/modal.css` (new), `web/templates/partials/modal_open.html` (new),
`web/templates/partials/modal_close.html` (new), `web/templates/layout.html`
(script/style includes), `web/render.go` (`dict` template helper if absent),
and the 12 `onsubmit="return confirm(...)"` call sites in
`history.html`, `index.html`, `items.html`, `plan.html`, `recipe.html`,
`settings.html`, `stores.html` → `data-confirm="..."`.

Three widths: dialog 32rem / form 44rem / large 52rem.
`goeat.confirm(msg, opts) -> Promise<bool>` and `goeat.alert(msg)` replace the
native calls; a global submit interceptor handles `data-confirm` forms.

```
web: add custom dialog and tooltip primitives, drop browser confirm()
```
Status: ✅

**Namespace note.** `.modal` already meant two different things in this repo
before the port: `.modal:not(dialog)` is a full-screen overlay (stores.html)
and `dialog.modal` is a native `<dialog>` panel (HA setup, price edit). The
new chrome therefore uses `.modal-overlay` + `.modal-dialog` (BEM children
`__header`/`__body`/`__footer`/`__close`), colliding with neither, and
`modal.js`'s `data-modal-open` handler only fires for overlays carrying
`data-modal` so stores.html's own opener is left alone. Commit 1b migrates
the stragglers.

---

### 1b. web: migrate legacy modals onto the shared chrome
Three modal conventions coexist after commit 1. This retires the older two so
`partials/modal_open.html` is the only one left: stores.html's
`hidden`-toggled `.modal__backdrop`/`.modal__panel` overlay with its
page-local opener script, and the native `<dialog class="modal">` panels
(`partials/ha_setup_dialog.html`, the price editor in
`partials/shopping_list_body.html`).

**Reduced scope**: stores.html and the `.modal:not(dialog)` overlay were done
in commit 4, which had to touch that file anyway. What is left is the two
native `<dialog class="modal">` panels.

Files: `web/templates/partials/ha_setup_dialog.html`,
`web/templates/partials/shopping_list_body.html` (the price editor),
`web/static/css/wizard.css` (remove `dialog.modal` rules).
Depends on commit 1.

```
web: migrate remaining modals onto the shared dialog chrome
```
Status: ⬜

---

### 2. web: debounced auto-filter and skeleton loading across list pages
Removes every explicit "Filter"/"Apply" button; filter inputs now submit on a
debounce (matching the settings-page autosave pattern already in `main.js`).
Every data page gets a skeleton state while its first paint is in flight.

Files: `web/static/js/main.js` (`autoFilter` module, debounce util reuse),
`web/static/css/components.css` (`.skeleton-*` extension: table rows, cards,
list rows), `web/templates/history.html`, `items.html`, `llm_log.html`,
`pantry.html`, `recipes.html`, `stores.html`, `admin_prices.html`,
`search_results.html`.

```
web: auto-filter list pages on debounce and drop Filter buttons
```
Status: ✅

**Scope note on skeletons.** These pages are server-rendered, so their data
is already in the first paint - there is nothing to skeleton on load. The
skeleton that matters is the one covering the debounced filter's round trip,
which is what shipped. Genuinely async fragments (dashboard hover cards,
About probes, chat) get theirs in commits 8, 9 and 11.

---

### 3. web: colored pills for item source, unit, and category
Source / unit / category render as a deterministically colored pill so a row
can be scanned by color. Colors derive from a stable hash of the value so new
sources/units don't need a palette entry, with hand-picked overrides for the
known set.

Files: `web/render.go` (`pillClass` helper), `web/static/css/components.css`
(`.pill--*` palette, light + dark), `web/templates/items.html`,
`item_detail.html`, `pantry.html`, `admin_prices.html`.

```
web: render item source, unit, and category as colored pills
```
Status: ✅

The source column was already reaching for `badge-cached`/`badge-manual`/
`badge-estimate`, none of which are defined in any stylesheet, so it rendered
uncolored. The 8-hue meal-tag palette is now the one shared palette
(`.badge-hue-1..8`), assigned in `web/pills.go` by hash with a few pinned
source colors, and the shopping list's meal tags moved onto it too.

---

### 4. web: modal add-item forms and quick add-to-list/on-hand actions
"Add item" moves out of the inline form into an upper-right button + modal on
both `/pantry` and `/pantry/items`, matching the recipes page. Each catalog
row gains a quick action next to Edit that opens a small dialog for quantity
plus optional price, writing either to the shopping list or to on-hand stock.
With no price entered, the existing pricing chain supplies one.

Files: `web/templates/items.html`, `pantry.html`,
`web/templates/partials/item_quick_add.html` (new),
`web/handlers_items.go` (`/pantry/items/{id}/quick-add`),
`web/handlers_pantry.go`, `web/routes.go`, `db/pantry.go`,
`db/shopping_list_items.go` (upsert-by-item helpers).
Depends on commit 1.

```
web: add item modals and quick add-to-list/on-hand actions
```
Status: ✅

**Absorbed commit 1b's stores.html migration.** stores.html carried its own
modal system - markup, opener, closer and Escape handler - and it was already
the last consumer of the `hidden`-toggled `.modal:not(dialog)` overlay. Moving
it onto the shared chrome in the same commit let that CSS be deleted rather
than left behind as a trap, so 1b is now only the two native `<dialog>`
panels.

**Price is optional and does not block on the network.** With the field left
blank the quick-add does *not* run the live pricing chain - a lookup takes
seconds and this is a button press - it falls back to a manual price, then the
price cache, then goes on unpriced with the line saying so. The normal
re-price fills it in.

**One handler for both destinations.** List and pantry share the item lookup,
the quantity handling and the price handling; only the final write differs, and
splitting them would mean maintaining that agreement twice. The two footer
buttons are both submits that set the destination, so choosing and committing
is one gesture.

---

### 5. db+web: item aliases and shopping-list link status
Pantry/catalog items gain an alias list so a shopping line's free-text name
resolves to a known item across plan rebuilds. Each shopping-list row shows a
link chip: **green** when linked to a catalog item, **yellow** when unmatched
and awaiting a manual match. Clicking a yellow chip opens a match dialog with
fuzzy-search suggestions; confirming records the alias.

Files: `db/migrations/00019_item_aliases.sql` (new), `db/item_aliases.go` (new),
`db/store.go`, `db/models.go`, `catalog/catalog.go` (alias lookup in
`EnsureItem`), `catalog/fuzzy.go` (new) + `catalog/fuzzy_test.go` (new),
`web/handlers_shopping.go` (`/list/{id}/match`),
`web/templates/partials/shopping_list_body.html`, `web/routes.go`,
`web/static/css/components.css`.
Depends on commits 1, 3.

```
db: add item aliases and surface shopping-list link status
```
Status: ✅

**What the chip colours actually mean.** `catalog.EnsureItem` *always* created
an item when a normalized term missed, so every line was technically "linked"
and there was no unmatched state to show. Green now means the line resolves to
a real catalog item (source builtin/manual - seeded, created, or confirmed);
yellow means it resolves only to a `source='auto'` placeholder, so its price
and pantry stock are being tracked under a name nobody confirmed.

**EnsureItem grew a third and fourth step**: alias lookup, then a fuzzy match
taken automatically only above `AutoLinkScore` (0.92) and recorded as an alias
so the next sighting is a hit rather than a re-score. The bar is high because
linking is a destructive merge and a wrong one is close to invisible
afterwards.

**Scoring is token overlap, not edit distance.** Grocery names differ by whole
words ("chicken breast" vs "boneless skinless chicken breasts"), where edit
distance is terrible and overlap is perfect. Note that `pricing.Normalize`
already collapses plurals and prep words, so the fuzzy step's real job is the
qualifiers it keeps - "extra virgin olive oil" normalizes to "virgin olive
oil", not "olive oil".

**Confirming a match merges, it does not just repoint.** Otherwise the
placeholder survives holding its own price history and keeps collecting future
ingredients with the same name, and the line comes back yellow next week. Only
`source='auto'` placeholders are merged away - merging two real items because
a line was mis-linked would destroy one the household deliberately created.

**Bug found:** `pantry_items.item_id` was write-only - `SetPantryItemItem` set
it and no SELECT ever read it back, so `PantryItem.ItemID` was nil everywhere
in the app. Two near-identical pantry scan functions had drifted; they are now
one.

---

### 6. web: fix $0.00 estimated total on the shopping list
Regression: `/plan/list` reports `$0.00 estimated total` even when lines carry
prices. Root cause to confirm in `web/handlers_shopping.go` — `p.TotalCents`
is read from the plan row rather than summed from the current lines, so it is
stale whenever lines are repriced without a plan-level recompute
(`recomputeTotals` at :594 writes it, but not on every path).

Also adds a per-line **"I already have this"** checkbox: ticking it excludes
that line from the estimated total and marks it as not needing to be bought,
distinct from the existing "checked off while shopping" state. Being distinct
matters - one means "already in the cupboard, never buy it", the other means
"picked up on this trip".

Files: `web/handlers_shopping.go`, `plan/…` reprice path,
`db/migrations/00022_list_have.sql` (new, `already_have` column),
`db/shopping_list_items.go`,
`web/templates/partials/shopping_list_body.html`,
`web/shopping_list_render_test.go` (regression test asserting a non-zero
total for priced lines, and that an already-have line is excluded from it).

```
web: recompute shopping list total from current lines
```
Status: ✅

**Root cause, confirmed.** `TotalLabel` read `p.TotalCents` from the plan row,
and only `UpdatePlanTotal` writes that - so the pencil price editor, a
headcount rescale, and `EnsureShoppingList`'s unpriced fallback all left it
stale at whatever it last was, usually 0. Now summed from the current lines by
`summarizeLines`, which cannot go stale by construction. Store subtotals go
through the same function, and a dead no-op loop that pretended to compute
them was removed.

**No migration needed for already-have.** `shopping_list_items.in_pantry` has
existed since `00005_pricing.sql`, with a store method and an HA-sync skip
already built on it - it was simply never surfaced or settable. This exposes
it as the "I already have this" control, so the line drops out of the
estimated total and its store subtotal while staying visible on the list.

**Stocking the pantry.** Marking a line adds it to the pantry with the
quantity from the dialog, but *only when it is missing*: `CreatePantryItem`'s
upsert adds to an existing quantity, so re-ticking a line the user already
tracks would silently inflate their stock each time. Un-ticking takes nothing
back out - you did have it.

Also fixes `handlePantryStock`, which multiplied `BuyQuantity` (already
packs x pack_size) by the pack size again and stocked several times what was
bought.

---

### 6b. db+web: household members and per-person portion sizing
A household is currently just a headcount, so a plan for "4" budgets the same
food whether that is four adults or two adults and two toddlers. This adds
named members, each with a **portion factor** — how much that person eats
relative to one standard adult serving (a small child ~0.5, a light eater
~0.8, a big eater ~1.4) — plus per-member dietary notes and dislikes that the
generator already accepts for the household as a whole.

The plan's serving math changes from *count of people* to *sum of portion
factors*, so Phase 8's base-value rescaling (commit 6 there) keeps working
unchanged: the scale factor is just computed from members instead of an
integer.

Surfaces:
* **Setup wizard** — a members step after household basics, seeded with one
  member for the operator, with an "everyone eats a standard portion" default
  so the step can be skipped.
* **Preferences page** — full CRUD on members (add/edit/remove, portion
  factor, notes), the canonical place to manage them afterwards.
* **Plan day widget** — the day's "people eating" control becomes a member
  multi-select (see commit 7); the numeric headcount stays as the derived,
  displayed value.

Files: `db/migrations/00018_household_members.sql` (new),
`db/household_members.go` (new), `db/household_members_test.go` (new),
`db/models.go` (`HouseholdMember`, `PlanDay.MemberIDs`), `db/store.go`,
`db/plan_days.go` (portion-factor sum feeds `ScaleMealsForDay`),
`web/handlers_preferences.go`, `web/handlers_setup.go`,
`web/templates/preferences.html`, `web/templates/setup.html`,
`web/templates/partials/member_form.html` (new), `web/routes.go`,
`plan/prompt.go` (per-member notes in the generation prompt),
`plan/generate.go`.
Depends on commit 1 (member add/edit dialogs).

**Migration numbering**: this takes `00018`, so commit 5's aliases move to
`00019` and commit 7's day status to `00020`.

```
db: add household members with per-person portion sizing
```
Status: ✅

**What actually shipped, vs. the sketch above.**

* No separate wizard step. Members went into the existing "Your household"
  step as a dynamic list of name + portion rows, which avoided renumbering
  steps 3-6 and reads better than splitting one topic across two screens. A
  `<noscript>` household-size field remains, since the rows are built
  client-side.
* `ScaleMealsForDay` now takes `portions float64` rather than
  `headcount int`. Ingredient quantities scale by the raw total (2.5 portions
  really is half of a 5-serving base) while `servings` and `cooked_portions`
  take the rounded value, because a recipe card cannot state 2.6 servings.
* `plan_days` stores both `member_ids` and the `portions` total. The total is
  stored rather than recomputed on read, so editing a member's factor next
  month does not silently restate what an existing plan was scaled to - there
  is a test for exactly that.
* The `/plan/days/{date}/headcount` endpoint accepts either `member` ids or a
  bare `headcount`, so commit 7 only has to change the form.

---

### 7. web: plan day headcount autosave and day-status actions
Two changes to the weekly-plan day widget:

* The "people eating" checkmark button is removed; the control autosaves on a
  debounce (same helper as commit 2). A `(?)` affordance replaces it, showing
  a tooltip explaining what the setting does (rescales servings and ingredient
  quantities from the as-generated base — see Phase 8 commit 6).
* With commit 6b landed, the control selects **which household members** are
  eating rather than typing a bare number; the day's scale factor is the sum
  of their portion factors and the numeric headcount is shown as derived.
* A dropdown next to "people eating" marks the day **eating out**, **skipped**,
  or **leftovers**. Because a marked day breaks any downstream leftovers link
  (dinner tonight → leftovers tomorrow), choosing one opens a resolution dialog
  offering: pick a replacement meal, mark the dependent day too, or leave the
  slot empty.

Files: `db/migrations/00020_day_status.sql` (new), `db/plan_days.go`
(`SetDayStatus`, dependent-day lookup), `db/models.go` (`PlanDay.Status`),
`db/plan_days_status_test.go` (new), `web/handlers_plan.go`,
`web/templates/plan.html`, `web/templates/partials/day_status_dialog.html` (new),
`web/routes.go`, `web/static/js/main.js`.
Depends on commits 1, 2, 6b.

```
plan: autosave day headcount and add eating-out/skip day status
```
Status: ✅

**Three statuses, not four.** A day you eat leftovers is already visible as
leftover meal cards on that day (`meals.is_leftover`), so `leftovers` as a
day status would be a second, separately-editable copy of the same fact. The
dropdown is cooking / eating out / skipped.

**Where the filter lives.** A non-cooking day contributes nothing to the
shopping list, and that is enforced once in `ListIngredientsByPlan` - every
list-building path (CostPlan, EnsureShoppingList, a reprice after a rescale)
reads its ingredients through it, so a day taken off the plan disappears from
the list whichever runs next. LEFT JOIN, so a plan with no day rows is not
silently emptied.

**Status is not on UpsertPlanDayParams**, deliberately: that upsert runs on
plan generation and on every people-picker save, and a status field on it
would default to "cooking" at each of those call sites and silently un-mark a
day. `SetPlanDayStatus` is separate, and there is a test for the interaction.

**Deferred to 7b: "pick a replacement meal".** The dialog offers *cascade the
status* and *clear the slot*, both complete. Materialising a replacement from
a saved recipe needs a recipe→meal path (meal + meal_recipe + scaled
meal_ingredients, then a reprice) that does not exist yet, and the same engine
is what commit 11's `swap_meal` tool needs - so it is built once, properly, in
its own commit rather than half-built here.

---

### 7b. plan: materialise a meal from a saved recipe
The one piece commit 7 left out: filling an emptied slot with an actual meal.

Creates a `meals` row plus its `meal_recipes` and `meal_ingredients` from a
`catalog_recipes` entry, scaled to the day's portion total, then reprices.
Surfaces as a "pick a replacement" option in the day-status dialog and as a
per-slot "add a meal" action on an empty card.

Built as its own commit because commit 11's `swap_meal` / `edit_meal` tools
need exactly this engine, and building it twice - once inline in a dialog,
once for the agent - is how the two drift apart.

Files: `plan/materialize.go` (new), `plan/materialize_test.go` (new),
`db/meals.go`, `db/meal_ingredients.go`, `web/handlers_plan.go`,
`web/templates/plan.html`, `web/routes.go`.
Depends on commits 6b, 7.

```
plan: create a meal from a saved recipe, scaled to the day
```
Status: ✅

**A quantity parser had to come first.** `catalog_recipe_ingredients.quantity`
is TEXT (what an import gives you: "1 1/2", "2-3", "½", "a pinch") while
`meal_ingredients.quantity` is a float to aggregate and price.
`plan.ParseQuantity` bridges them. Two deliberate calls: a range takes its
**low** end, because shopping one clove short is recoverable and rounding
every range up all week is how a budget-first planner quietly overspends; and
text with no number in it returns `ok=false` rather than 0, because a silent 0
would price a whole ingredient at nothing. Unquantified lines are still added
to the meal - dropping an ingredient silently is worse - and the caller is
told which ones so the user can fill them in.

**`SetMealBaseline` was the missing piece.** A meal created outside plan
generation has zero in its `base_*` columns, and `ScaleMealsForDay` scales
from those - so the first headcount change would find nothing to scale from
and skip the meal entirely. The as-created amounts are now written as the
baseline.

**Also reachable from an empty slot**, not only from the day-status dialog: an
empty meal card gets an "Add a meal" button. The "replace" resolution clears
the orphans and returns with `?fill=date|slot`, which reopens the picker aimed
at the emptied slot - the meals have to be gone before a replacement is
chosen, so it cannot happen in one request.

---

### 8. web: dashboard meal hover cards and stats-chip cleanup
Hovering a food item on the dashboard shows a preview card (photo, title,
cost, ingredient list), following the knowledge-graph hover card in
`slack-llm-proxy`. Also removes the text background behind the
"This week spent" stats chip value.

Files: `web/static/js/hovercard.js` (new), `web/static/css/components.css`,
`web/templates/index.html`, `web/handlers.go` (`/meal/{id}/card` fragment),
`web/routes.go`.
Depends on commit 1 (tooltip layer).

```
web: add dashboard meal hover cards and clean up stats chip styling
```
Status: ✅

**The chip's background was `badge--success`/`badge--danger`**, which paint a
tinted block behind the value inside a chip that already has its own surface -
two nested backgrounds for one number. Replaced with classes that colour the
text only.

**Cost on the card is attributed, not claimed.** A shopping line is shared
between every meal using that ingredient, so a meal cannot take the whole line
- two meals using the same onions would each claim the bag. The line is split
evenly across the meals referencing it, and the card says "about" rather than
presenting an approximation as exact. Already-have lines are excluded.

**Photos come from the saved recipe, matched by title.** A meal has no image of
its own, but commit 10 files every generated meal in the recipe catalog under
the same title, and that row can have one. Title matching is loose on purpose:
a wrong photo on a hover card costs nothing, and requiring a hard link would
mean no photos at all until every meal carried a recipe id.

**Bug found:** `index.html` did `index $cal.Rows 0` unguarded, and `index` on
an empty slice errors the *whole page render*, not just that line - a calendar
with no rows would have taken the dashboard down with it.

---

### 9. web: About page connectivity widget accuracy and live updates
Drops the green border behind the connectivity text (colored text stays),
makes each row reflect a real probe result rather than configuration
presence, and streams updates over SSE with a short polling fallback.

Files: `web/handlers_about.go` (probe funcs + `/about/stream`),
`web/handlers_about_test.go`, `web/templates/about.html`,
`web/static/css/components.css`, `web/routes.go`.

```
web: make About connectivity status live and accurate
```
Status: ⬜

---

### 10. plan: persist AI-generated recipes and auto-create catalog items
Every recipe the generator produces is saved to the recipe catalog at
generation time rather than only living on the plan. The system prompt is
extended so the model returns enough structure to populate a recipe row and
to match ingredients against the item catalog; unmatched ingredients create a
catalog item via `catalog.EnsureItem`.

Files: `plan/prompt.go`, `plan/generate.go`, `plan/types.go`,
`plan/generate_test.go`, `db/catalog_recipes.go`, `catalog/catalog.go`,
`llm/generator.go`.
Depends on commit 5 (aliases).

```
plan: save generated recipes and auto-create catalog items for ingredients
```
Status: ✅

**Bug found while wiring this.** Ingredient linking ran *only* inside the
pricer, and `buildPricer` returns nil when no store chain is configured - so a
household with no stores never linked a single ingredient, and every shopping
line stayed unmatched forever. Linking moved into `persistPlan`, where it
happens as each ingredient is created regardless of pricing. It now goes
through commit 5's `EnsureItem`, so generation reuses aliases and confident
fuzzy matches instead of minting a placeholder per name.

**Recipe metadata had to be asked for.** `GeneratedMeal` gained
`prep_minutes`, `cook_minutes`, and `tags`; without them every catalog entry
would land with no times and no way to find it. All three are optional in the
response, so a model that ignores them still produces a valid plan. The prompt
also now insists ingredient names be the plain grocery name ("chicken breast",
not "boneless skinless organic chicken breast, cubed") - a name loaded with
adjectives matches nothing in the item catalog.

**Duplicate titles are skipped, not overwritten.** The same meals come back
week after week; the existing entry is left alone because it may have been
edited by hand since.

---

### 11a. agent: tool registry and run loop
A floating circular launcher in the bottom-right of every page opens a chat
panel. The assistant is backed by an MCP-style tool registry over the site's
own data so "move chicken quesadillas to monday" reshuffles the plan.

Tool surface (granular, one verb per tool):
`read_plan`, `read_meal`, `move_meal`, `edit_meal`, `delete_meal`,
`swap_meals`, `set_day_status`, `set_day_headcount`,
`read_shopping_list`, `add_list_item`, `remove_list_item`, `set_item_price`,
`read_pantry`, `set_pantry_qty`, `search_items`, `search_recipes`.

Each tool is a Go func with a JSON schema, household-scoped, audit-logged, and
mutating tools return a diff the UI can render for confirmation.

Files: `agent/registry.go`, `agent/tools_plan.go`, `agent/tools_shopping.go`,
`agent/loop.go`, `agent/agent_test.go` (all new), `db/meals.go` (`MoveMeal`),
`db/shopping_list_items.go` (`DeleteShoppingListItem`), `db/store.go`.
Depends on commits 1, 7, 7b.

```
agent: add granular tool registry and run loop for the assistant
```
Status: ✅

**Split from the UI.** The registry is the substance of this feature and
deserves reviewing on its own; 11b is the widget that drives it.

**A JSON protocol, not provider-native tool calling.** This app talks to
Anthropic and to any OpenAI-compatible endpoint, including local reasoning
models the `llm` package already works around (`llm/openai.go` strips
`<think>` blocks and suppresses `enable_thinking`). Native tool-use is spelled
differently on each and is missing or broken on several; "reply with one JSON
object" works on all of them and is straightforward to swap out later.

**Granular by design.** One verb per tool, 16 of them, each household-scoped
and individually described. A single `update_plan` taking a free-form patch
would be shorter and far worse: nothing to constrain the model to, a whole
week rewritable by one bad call, and nothing specific enough to show a user
before applying.

**Safety properties worth naming.** `MaxSteps` caps a run at 8 tool calls, so
a confused model stops rather than churning writes. Ambiguous names error with
the candidates rather than guessing - picking one of two meals and then moving
it is not recoverable. Tool errors are fed back to the model, not the user,
since most are things it can fix itself. Every call is audited including the
failures, because "it tried and could not" is what a user needs when an answer
looks wrong.

**Bug found:** `MoveMeal`'s swap needed three writes, not two.
`UNIQUE(plan_id, day, slot)` is checked per row as each UPDATE runs, so moving
the displaced meal into the mover's place collided with the mover still
sitting there. The displaced meal is parked on a sentinel day first.

---

### 11b. web: site-wide chat widget
A floating circular launcher in the bottom-right of every page, opening a chat
panel backed by the 11a registry. Streams over SSE, renders the audit trail so
a user can see the work rather than trusting a summary, and keeps conversation
history.

Files: `web/handlers_chat.go` (new), `web/routes.go`,
`web/templates/partials/chat_widget.html` (new), `web/static/js/chat.js` (new),
`web/static/css/chat.css` (new), `web/templates/layout.html`,
`db/migrations/00021_chat.sql` (new, conversation history).
Depends on commit 11a.

```
web: add the site-wide AI chat widget
```
Status: ✅

**Not on the shared modal chrome.** A modal takes the page away and makes
`<main>` inert, and the whole point of this panel is to sit beside the plan you
are talking about while it changes underneath you.

**SSE carries progress, not tokens.** The agent loop makes whole `Generate`
calls, so there is no token stream to forward; pretending otherwise would be a
lie in the UI. What streams is one event per tool call as it completes, via a
new `Session.OnStep` hook - so a user watching "Moved Chicken Quesadillas to
monday dinner." appear knows immediately that they were understood. `fetch`
with a hand-rolled SSE reader rather than `EventSource`, which is GET-only and
cannot carry the CSRF header.

**The page is reloaded on close, not mid-turn.** A turn that changed data
leaves the page behind it stale; reloading while someone is still typing would
be worse than the staleness.

**Bug found:** nothing in the app rendered a `#csrf-token` element, so
`main.js`'s `goeatCSRF()` - used by the autosave forms - had been returning
`""` since it was written. The layout now provides it for signed-in users.

---

### 12. web: mobile and desktop layout pass
A responsive review of every page: tables that overflow on narrow screens get
a card or scroll treatment, the plan grid reflows, dialogs and the chat panel
size correctly on small viewports, and touch targets meet 44px.

Files: `web/static/css/layout.css`, `components.css`, `chat.css`, `modal.css`,
plus per-page tweaks across `web/templates/`.

```
web: responsive layout pass for mobile and desktop
```
Status: ⬜

---

## Summary

| # | Commit | Depends on |
|---|--------|-----------|
| 1 | ✅ Dialog + tooltip primitives | — |
| 1b | Migrate legacy modals | 1 |
| 2 | ✅ Auto-filter + skeletons | — |
| 3 | ✅ Colored pills | — |
| 4 | ✅ Item modals + quick add (absorbed 1b's stores.html) | 1 |
| 5 | ✅ Item aliases + link status | 1, 3 |
| 6 | ✅ Shopping total fix + already-have | — |
| 6b | ✅ Household members + portion sizing | 1 |
| 7 | ✅ Day headcount autosave + day status | 1, 2, 6b |
| 7b | ✅ Meal from saved recipe | 6b, 7 |
| 8 | ✅ Dashboard hover cards + chip cleanup | 1 |
| 9 | About connectivity live | — |
| 10 | ✅ Persist generated recipes | 5 |
| 11a | ✅ Agent tool registry + loop | 1, 7, 7b |
| 11b | ✅ Chat widget | 11a |
| 12 | Responsive pass | all |

Migrations are claimed in commit order: `00018` household members (6b),
`00019` item aliases (5), `00020` day status (7), `00021` chat history (11).

Commits 1–3 are the shared platform and should land first. Commits 6 and 9 are
independent bug fixes and can be pulled forward if a release is needed sooner.
Commit 11 is the largest single piece and assumes 1 and 7 are settled.

Commit messages carry no AI attribution.
