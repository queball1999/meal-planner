# Phase 11 Commit Plan — Pack-Unit Reconciliation, Guests, Kroger Rotation, Account Page

Date: 2026-09-08
Spec ref: §5.6 (leftovers/guests), §6.2 (Kroger/OfficialAPIProvider), §6.4 (pricing), §7.4 (generation), §9.3/§10.1 (auth)
Baseline: `ee845ae` (Phase 10 complete, tree clean)

Ten commits. Two ordering constraints: commit 2 (catalog hints) must land before
commit 3 (which wires them into `plan/generate.go`), and commit 5 (db guests)
before commit 6 (which calls `ScaleMealsForDayBySlot`). Every other commit is
independent. A few files carry hunks for two commits (`db/store.go`,
`web/templates/layout.html`, `web/static/js/main.js`,
`web/static/css/components.css`); those are hunk-split and noted per commit.
Legend: ✅ done · ⬜ not started

Exclude from every `git add`:

- `bin/` (build artefact)
- `data/` (SQLite runtime files — WAL, SHM)

---

## Commits

### 1. pricing: reconcile pack units to the item's stock unit
**This is a bug, not a feature.** `CostPlan` divided `TotalQuantity` (held in
the item's stock unit — grams) by `PackSize` (held in the store's unit —
pounds) and called the quotient a pack count. A 680 g chicken breast priced by
the pound bought "681 lb"; nine eggs priced per carton bought "9 cartons."
Multiplying across two unrelated units is arithmetic on incompatible numbers,
the same failure `ApplyPantry` refuses to make.

The fix is a conversion, not a guess: `reconcilePack` expresses the pack in the
item's stock unit on the conversion graph, then works out how many whole packs
cover the need. When the two units do not connect on the graph, it does **not**
invent a pack count — it buys the recipe amount as-is, prices it from the LLM's
own per-quantity estimate where there is one, and flags the line an estimate.
Guessing is exactly how "9 cartons of eggs" happened.

The shopping-list label reads in the stock unit too (`BuyQuantity` is stored in
it), and `qtyLabel` renders quantities the way a person says them: "4 each" →
"4", "2 slice" → "2 slices", "1.5 kg" untouched.

Files: `pricing/costing.go`, `pricing/costing_test.go`, `web/format.go` (new) +
`web/format_test.go` (new), `web/render.go`, `web/handlers_shopping.go`,
`web/handlers_meal_card.go`, `web/templates/meal.html`.

```
pricing: reconcile pack units to the item's stock unit
```
Status: ⬜

**Where it landed.** `reconcilePack` runs once per priced line, after the chain
has produced a pack in whatever unit the source used. `reconciled=false` means
"do not trust packs/buyQty" — the caller falls back to the recipe amount and the
estimate badge, never to a fabricated pack count.

**Two regressions, both tested.** `TestCostPlan_ReconcilesPackUnitToStockUnit`
is the 681 lb case (680 g need, 1 lb pack → 2 packs ≈ 907 g, $8).
`TestCostPlan_UnreconcilablePackDegradesToEstimate` is the 9-cartons case (each
vs carton, no edge → buy 9, price the LLM's per-quantity guess, flag estimate).

**`qtyLabel` is display, not data.** It only changes how a number reads; the
stored `BuyQuantity`/`PurchaseUnit` are untouched. "each" and its synonyms
collapse to the bare count because the item name beside it already says what is
being counted.

---

### 2. catalog: seed new items with the generator's unit and conversions
A brand-new catalog item used to default to stock unit "each" with no
conversions, whatever the recipe actually measured in. Flour bought by the gram
landed as "each"; a clove of garlic had no path to grams at all, so costing
could only guess.

`EnsureItemWithHint` takes the model's knowledge (`ItemHint`: the purchasable
stock unit plus any item-specific edges like "1 clove = 5 g"). A new item takes
the hint's unit as its stock unit and gets its derived one-hop conversion table
built immediately, so costing and the item page never BFS at read time. For an
existing item the hint only enriches conversions — and **never overwrites a
hand-entered edge**, because a wrong model guess there is a duplicate someone
can fix, not a price they can eat.

Files: `catalog/catalog.go`, `catalog/catalog_test.go`, `plan/types.go`,
`plan/prompt.go`.

```
catalog: seed new items with the generator's unit and conversions
```
Status: ⬜

**`EnsureItem` is now a thin wrapper** over `EnsureItemWithHint` with an empty
hint, so existing callers are unchanged and the no-hint path is the same code.

**Hint edges are item-specific and additive.** They are written only where the
item does not already have that pair (either direction), and `RecalcItemConversions`
rebuilds the derived table afterwards. `TestEnsureItemWithHintDoesNotClobberManualConversion`
pins the no-overwrite rule.

**`plan/types.go` + `plan/prompt.go`** add `ItemUnit`/`Conversions` to
`GeneratedIngredient` and teach the model to emit them. This commit does not yet
*use* the hint in `persistPlan` — that wiring lands in commit 3, which is why
this commit must come first.

---

### 3. plan: clamp implausible quantities and pass unit hints to the catalog
Two generation hardenings in `persistPlan`'s path. `ClampQuantities` pulls any
ingredient quantity past a sane per-line cap (scaled by the meal's serving
count) back down to that cap — clamp, not reject, because a 21-meal plan is
expensive to regenerate and one bad number should not throw the other 20 away.
Each clamped line is logged so an implausible model response is visible rather
than silently shopped.

The same commit wires commit 2's hint into item creation: `itemHintFrom`
carries the model's `ItemUnit`/`Conversions` into
`catalog.EnsureItemWithHint`, so a new item lands with the right unit and a full
conversion table instead of "each" with none.

Files: `plan/sanitize.go` (new) + `plan/sanitize_test.go` (new), `plan/generate.go`.

```
plan: clamp implausible quantities and pass unit hints to the catalog
```
Status: ⬜

**Caps are deliberately generous.** `perLineUnitCap` exists to catch "681 lb
chicken breast" and "9 dozen eggs" for a weeknight dinner, not to second-guess a
large-batch recipe. A unit not listed is left alone.

**Non-fatal by design.** `ClampQuantities` returns notes; `Generate` logs them
and continues. The same reasoning as `Validate` tolerating "0 pinch salt."

**Depends on commit 2** — `generate.go` calls `catalog.EnsureItemWithHint`,
which does not exist until the catalog commit lands.

---

### 4. plan: keep leftovers to the next day and out of breakfast
`PlanLeftovers` matched a surplus to *any* later slot whose portion count fit,
so Friday's oatmeal could be tagged "leftovers from Monday's chili" purely
because the numbers lined up — the source of "the recycle icon is on meals that
aren't leftovers."

A slot is now eligible only when it is lunch or dinner (nobody plans last
night's stir-fry for breakfast) **and** within `leftoverMaxDayGap` (one day) of
the meal that cooked the surplus. `dropStale` prunes the pool as the timeline
advances so a Monday batch-cook cannot reach across the week.

Files: `plan/leftovers.go`, `plan/leftovers_test.go` (new).

```
plan: keep leftovers to the next day and out of breakfast
```
Status: ⬜

**Spec §5.6 is "the next day's lunch or dinner."** The gap and the breakfast
exclusion are that sentence made enforceable.

**Unparseable days are skipped, not mis-linked.** A meal whose day cannot be
placed on the timeline is left alone rather than matched against the wrong
neighbour.

---

### 5. db: store guests and guest slots on a plan day
A day's headcount was only ever the household's own members. Guests are the
people on top of that — a dinner party, a friend staying over — and each eats a
standard portion. Stored as a count (not folded into `portions`) so the plan
page can re-render the "Guests × N" control with its number, and so editing a
member's portion factor later does not silently restate how many guests a past
day had.

`guest_slots` is the comma-separated set of slots the guests are counted for;
empty means every slot (the back-compatible default). `ScaleMealsForDayBySlot`
scales the whole day to a base portion count but moves the named slots to their
own number — which is how guests limited to dinner end up on dinner's plate and
nowhere else.

Files: `db/migrations/00023_plan_day_guests.sql` (new),
`db/migrations/00024_plan_day_guest_slots.sql` (new), `db/models.go`,
`db/plan_days.go`, `db/store.go` (the `ScaleMealsForDayBySlot` interface line
only — the `UpdateUserPassword` line belongs to commit 9),
`db/household_members_test.go`, `db/scale_day_test.go`.

```
db: store guests and guest slots on a plan day
```
Status: ⬜

**`guestSlotsJoin` normalises to canonical order** and collapses a full set to
`""`, so "guests eat every meal" has one representation, not two.
`parseGuestSlots` is its inverse; `nil` reads as "no per-slot restriction."

**`ScaleMealsForDay` is unchanged** — it delegates to the new `scaleDay` with a
nil slot map, so existing callers keep working.

---

### 6. web: count guests per day, optionally per slot
The people picker on the plan page gains a "Guest × N" row (a checkbox that
mirrors the count plus a −/+ stepper) and a "Guests eat" slot row shown only
while the day has at least one guest. `dayPortions` resolves the submission into
a `dayPortionInput` (member portions, guest count, guest slots, headcount), and
`scaleDayForInput` rescales the day to the household's own portions while the
guest slots also carry the guests. A guests-only day is still valid.

Files: `web/handlers_plan.go`, `web/templates/plan.html` (the guest-picker
block; the file's button/badge class renames ride along here),
`web/static/js/main.js` (the `initGuestPicker` block only — the header-search
block belongs to commit 8), `web/static/css/components.css` (the
`.people-picker__guest*` rules only).

```
web: count guests per day, optionally per slot
```
Status: ⬜

**Guests add 1.0 portion each**, on top of the ticked members, and 1 to the
headcount. Capped at 50 server-side.

**Dropping to zero guests clears the slot restriction** so it does not resurface
next time guests are added.

**Depends on commit 5** — `scaleDayForInput` calls
`store.ScaleMealsForDayBySlot`.

---

### 7. pricing: rotate Kroger credentials, rate-limit, and retry
One Kroger key can be throttled or spend its 10k/day Products quota, and the
old client had no limiter, no retry, and nowhere to go when that happened.

The provider now holds a **pool** of `{clientID, clientSecret}` pairs, each with
its own cached token and its own daily-call budget. Calls pass through a rolling
one-minute limiter; transient `429`/`5xx` are retried in-transport with backoff
that honours `Retry-After`. On a `429`, auth failure, or daily-cap exhaustion the
client parks that key and rotates to the next; when every key is unavailable the
tier yields to the next pricing tier — it never blocks the request.

`config` parses comma-separated, position-aligned `KROGER_CLIENT_ID` /
`KROGER_CLIENT_SECRET` lists into `KrogerCredentials` (a single value on each
line is the normal one-credential setup); `settings.Apply` rebuilds the pool
after either field changes.

Files: `pricing/kroger.go`, `pricing/kroger_test.go` (new), `config/config.go`,
`settings/registry.go`, `web/server.go`, `.env.example`, `README.md`.

```
pricing: rotate Kroger credentials, rate-limit, and retry
```
Status: ⬜

**Retries re-enter the shared limiter**, so a 429 storm cannot fan out
uncounted requests.

**A network error or parse failure is not a credential problem** — it does not
park the key or thrash the rest of the pool; the tier simply fails soft.

**`Retry-After` is clamped** (`krogerRetryAfterCap`) so one hostile header
cannot stall a request past the HTTP timeout.

---

### 8. web: popover primitive for mobile nav and search
The mobile nav was a `max-height` drawer that pushed the header taller, and the
search field had a submit button on desktop and a cramped input on mobile.

`popover.js` is one floating-panel primitive for the whole app: opens from a
trigger, floats above content (fixed position, its own stacking context),
closes on outside-click / Escape / a second press, and repositions on
resize/scroll. The mobile nav menu and the mobile search both ride on it, so
there is one set of open/close/focus/dismiss rules, not three. The search field
becomes a shared partial rendered inline on desktop and inside the `#searchPop`
popover on mobile, both with the same debounced auto-submit and no submit
button.

`modal.js` hoists the overlay to `<body>` on open: the overlay markup is emitted
inside `<main>`, and since `<main>` is marked `inert` while a modal is open, a
modal left in that subtree went inert too — every button dead, Escape included.

Files: `web/static/js/popover.js` (new), `web/static/css/popover.css` (new),
`web/templates/partials/nav_items.html` (new),
`web/templates/partials/search_field.html` (new), `web/templates/layout.html`
(the nav/search popover wiring only — the account link belongs to commit 9),
`web/static/js/main.js` (the `initHeaderSearch` block only),
`web/static/css/layout.css`, `web/static/js/modal.js`.

```
web: popover primitive for mobile nav and search
```
Status: ⬜

**The nav links live in one partial** (`nav_items.html`) rendered with two class
sets, so the active-page highlight is kept in one place instead of two copies of
six links.

**One popover at a time** — opening a second closes the first.

---

### 9. web: self-service account page
Password change, data export, and the two destructive resets were scattered on
the Settings danger panel. They now live on `/account`, reachable from the
header (the username is a link; the inline sign-out form moves onto the page).

Password change verifies the current password, enforces the strength rule,
refuses a no-op, then signs out **every** session for the user so a changed
password actually locks out anyone who had one. Export streams a single JSON
document of everything the household owns. Reset clears plans + shopping lists
(typed `RESET`); wipe clears all content (typed `DELETE`) — both confirm words
are enforced server-side, not just in the page.

Files: `web/handlers_account.go` (new), `web/account_render_test.go` (new),
`web/templates/account.html` (new), `web/routes.go`, `db/users.go`,
`db/store.go` (the `UpdateUserPassword` interface line only), `web/icons.go`,
`web/templates/layout.html` (the user-menu → account link only),
`web/static/css/components.css` (the `.user-menu__name` link and
`.form-section--danger` rules only).

```
web: self-service account page
```
Status: ⬜

**`UpdateUserPassword` returns an error when no row matched**, so a stale user
ID cannot silently "succeed."

**The confirm words are case-sensitive and trimmed**, checked in the handler —
both endpoints are reachable without the page.

---

### 10. web: align template class names with the stylesheet
The stylesheet already uses `.btn-primary`, `.badge-muted`, `.badge-accent`,
`.btn-ghost`, `.btn-secondary`, `.btn-danger`; a batch of templates still used
the old `.btn--primary`, `.badge--muted`, `.badge--dim`, `.badge--live`,
`.btn--ghost`, `.btn--secondary` names, so those buttons and badges rendered
unstyled. This commit aligns the templates to the CSS that is already there, and
folds in the mobile shopping-list layout polish (bigger tap targets, the
quantity/total row split, the store-subtotal wrap).

Files: `web/templates/admin_prices.html`, `web/templates/history.html`,
`web/templates/index.html`, `web/templates/instore.html`,
`web/templates/pantry.html`, `web/templates/partials/pagination.html`,
`web/templates/preferences.html`, `web/templates/recipe_import.html`,
`web/templates/recipes.html`, `web/templates/scan.html`,
`web/templates/search_results.html`, `web/templates/settings.html`,
`web/templates/stores.html`, `web/static/css/components.css` (the
`.shopping-item` mobile rules only).

```
web: align template class names with the stylesheet
```
Status: ⬜

**Pure rename + layout; no logic.** `meal.html` and `plan.html` also carry
renames but land in commits 1 and 6 with their feature changes, so they are not
repeated here.

---

## Suggested order

```
1  pricing: reconcile pack units to the item's stock unit
2  catalog: seed new items with the generator's unit and conversions
3  plan: clamp implausible quantities and pass unit hints to the catalog   (needs 2)
4  plan: keep leftovers to the next day and out of breakfast
5  db: store guests and guest slots on a plan day
6  web: count guests per day, optionally per slot                          (needs 5)
7  pricing: rotate Kroger credentials, rate-limit, and retry
8  web: popover primitive for mobile nav and search
9  web: self-service account page
10 web: align template class names with the stylesheet
```

`make check` should pass after every commit; the only hard ordering constraints
are 2 → 3 and 5 → 6.
