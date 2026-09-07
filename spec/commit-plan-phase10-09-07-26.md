# Phase 10 Commit Plan — Pantry Deduction, Price Trends, In-Store Mode & Agent Confirmation

Date: 2026-09-07
Spec ref: §5.4 (pantry/reuse), §6.5 (caching & refresh), §13 (stretch goals)
Baseline: `f15809d` (Phase 9 complete, tree clean)

Four commits, each independent of the others. Legend: ✅ done · ⬜ not started

---

## Commits

### 1. pricing: deduct pantry stock from the shopping list
**This is a bug, not a feature.** `/pantry` tells the user "Pantry items are
deducted from future shopping lists" and spec §5.4 promises the same, but
`ListPantryItems` appears nowhere in `pricing/` — nothing has ever subtracted
anything. A household that has just stocked up is told to buy it all again,
and the budget the whole app is built around is overstated by whatever is
already in the cupboard.

The seam is `AggregateByItem`: it already resolves every ingredient to a
catalog item and expresses the total in that item's stock unit, which is the
same unit the pantry row is held in. Deduction is therefore a subtraction on
the aggregate, before pricing — not a filter afterwards, which would leave the
line priced and then hide it.

Rules that matter:
* Deduct only what is genuinely matched — same catalog item, or the same
  normalized term when neither side is linked. A name-similarity guess here
  would silently stop someone buying dinner.
* A partial cover reduces the quantity; a full cover marks the line
  `in_pantry` rather than deleting it, reusing the state commit 6 of Phase 9
  built. The line stays visible with what the pantry supplied, so a user can
  see *why* something is not being bought.
* Never deduct from an `Unconverted` aggregate: if the quantities could not be
  unit-reconciled, subtracting one from the other is arithmetic on
  incompatible numbers.
* Deduction is recorded per line, so the shopping list can say "2 of 3 lb from
  your pantry" rather than silently showing a smaller number.

Files: `db/migrations/00022_pantry_deduction.sql` (new, `pantry_qty_used` on
`shopping_list_items`), `pricing/pantry.go` (new) + `pricing/pantry_test.go`
(new), `pricing/costing.go`, `db/models.go`, `db/shopping_list_items.go`,
`web/handlers_shopping.go`, `web/templates/partials/shopping_list_body.html`.

```
pricing: deduct pantry stock from the shopping list
```
Status: ✅

**Where it landed.** `ApplyPantry` runs on the aggregate in `CostPlan`, before
pricing, and its result is carried onto each line as `pantry_qty_used` plus
the existing `in_pantry` flag. A fully covered line is excluded from the plan
total by the same rule "I already have this" follows.

**Per-row accounting, not per-match.** Stock is tracked against the pantry row
and spent down, so two aggregates matching the same row cannot both claim it -
without that, one jar of butter covers two recipes and the household ends the
week with none. There is a test.

**Conversion factor is derived from the row's full quantity**, not from what
is left. `Convert` is linear so the factor is constant, and computing it from
the remaining amount instead would drift on every second deduction from the
same row.

**Deliberately exact matching**: catalog item, else normalized term, and a
pantry row linked to a *different* item is not a match however similar the
names. No fuzzy matching, unlike `catalog.EnsureItem` - a wrong guess there
makes a duplicate someone can fix, a wrong guess here silently stops a
household buying dinner.

---

### 2. web: item price history chart and cheap-right-now flag
`price_history` has existed since `00014_price_history.sql` and every operator
price write lands in it, but only one modal ever reads it — the data has been
accumulating for nothing.

Adds a sparkline-style chart to the item detail page (inline SVG, no chart
library: one series of at most a few dozen points does not justify a
dependency, and an inline SVG themes itself with the rest of the app), plus a
"cheapest in N weeks" marker on a shopping-list line whose current price is
meaningfully below its own recent median.

The comparison is against that item's own history, never against a
cross-store average: two stores' prices for the same thing are not
comparable, and "cheap" against a blend of them would be noise.

Files: `db/price_history.go`, `db/store.go`, `web/handlers_items.go`,
`web/templates/item_detail.html`, `web/sparkline.go` (new) +
`web/sparkline_test.go` (new), `web/handlers_shopping.go`,
`web/templates/partials/shopping_list_body.html`,
`web/static/css/components.css`.

```
web: chart item price history and flag a good price
```
Status: ✅

**One chart per store, not one combined line.** Two shops' prices for the same
thing are not the same measurement, and averaging them draws a trend no shelf
anywhere ever had.

**Prices are normalised to cents per unit of amount** before charting or
comparing (`unitPrice`), so a change of pack size does not read as a change of
price - and a 5 lb bag does not look dearer than a 2 lb one.

**Median, not mean.** One mistyped price - a $40 onion - drags a mean far
enough to mislabel every price after it, and hand-entered price history is
exactly where that happens. There is a test with a typo in the series.

**Silence is the common case.** A verdict shows only at ±10% off the median
and only with at least three prior readings; with two, one outlier *is* the
median. Most prices are ordinary, and a badge on every line says nothing.

**Two points minimum for a chart.** One reading is a number, not a trend, and
a single-point chart invites the reader to see a shape that is not there. A
flat series is drawn level rather than stretched to fill the height.

---

### 3. web: in-store shopping mode
A view for the twenty minutes the list actually exists for. Big tap targets,
one item per row, grouped by store section in the order a shop is walked, a
running total of what is in the cart, and a wake lock so the screen does not
sleep between aisles.

Section order comes from the item's category, mapped to a walking order
(produce → bakery → deli → meat → dairy → frozen → pantry → household), with
anything uncategorised last rather than first — an unknown item at the top of
the list is the one thing guaranteed to be in the wrong place.

Files: `web/handlers_instore.go` (new) + `web/instore_test.go` (new),
`web/templates/instore.html` (new), `web/routes.go`,
`web/static/css/instore.css` (new), `web/static/js/instore.js` (new),
`web/templates/partials/shopping_list_body.html` (entry point).

```
web: add in-store shopping mode
```
Status: ✅

**A separate page, not a mode toggle.** The two views want opposite things:
the planning list is dense, sortable by store, and full of controls for
editing prices and matching items; this one wants one item per row, targets a
thumb can hit while pushing a trolley, and nothing that can be tapped by
accident.

**The whole row is the button.** A checkbox you have to hit precisely is the
wrong target for someone holding a trolley.

**The tick is optimistic.** It flips locally first and tells the server after,
rolling back with a message on failure - a round trip over shop wi-fi is not
something to wait on at the shelf.

**A finished aisle dims but stays reachable.** Collapsing it away would hide a
mis-tap at exactly the moment it needs undoing.

**Frozen is matched ahead of the aisle table**, because a frozen product names
two categories at once ("frozen vegetables") and a plain walk down the table
would file it under produce. Uncategorised items sort *last*, not first: an
item nobody has categorised is the one most likely to be somewhere unexpected,
and leading with it sends a shopper to the wrong end of the shop.

**Wake Lock is best-effort.** It needs a secure context and is missing on
several browsers; a page that refused to work without it would be worse than
one that occasionally dims. Re-acquired on `visibilitychange`, since the lock
is dropped whenever the tab is hidden and would otherwise survive exactly one
glance away.

---

### 4. agent: confirm destructive tool calls before applying
`Tool.Mutates` was defined in Phase 9 commit 11a and never used. The
assistant currently rewrites a plan with no preview and no undo — the one
thing about it that should make an operator nervous.

A mutating call is prepared, described, and held: the chat shows what it will
do and applies it only on confirmation. Read tools are unaffected, so
"what's on the list?" stays a single frictionless turn.

Confirmation is per *call*, not per turn: a run that moves three meals asks
three times rather than presenting one opaque "apply 3 changes?", because the
whole point is being able to reject one of them.

Files: `agent/confirm.go` (new) + `agent/confirm_test.go` (new),
`agent/registry.go`, `agent/loop.go`, `web/handlers_chat.go`,
`web/static/js/chat.js`, `web/static/css/chat.css`.

```
agent: hold destructive tool calls for confirmation
```
Status: ⬜

---

## Summary

| # | Commit | Depends on |
|---|--------|-----------|
| 1 | ✅ Pantry deduction | — |
| 2 | ✅ Price history chart | — |
| 3 | ✅ In-store mode | — |
| 4 | Agent confirmation | — |

All four are independent. Commit 1 is a correctness fix to a claim the UI
already makes and should land first.

Migrations claimed: `00022` pantry deduction.

Commit messages carry no AI attribution.
