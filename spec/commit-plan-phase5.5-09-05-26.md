# Phase 5.5 Commit Plan — Dashboard, Calendar & History
Date: 2026-09-05
Spec ref: §5.5, §8.2, §10.2, §11.1, §16.5

Phase 5.5 upgrades the placeholder dashboard into a real planning hub: live
spend stats for the current week, a navigable plan history table, and the
canonical week-boundary helper that every date-math call will read from.
Past plans are viewable in read-only calendar mode via `/plan?week=YYYY-MM-DD`.

The `plans.total_cents` column is already written by the pricing engine —
all history reads are stable, no re-pricing needed.

---

## Commits

### 1. db: add plan history and range queries
Files: `db/plans.go`, `db/store.go`

Add to `Store` interface and implement:
- `ListPlans(ctx, householdID int64) ([]*Plan, error)` — all plans newest first
- `ListPlansInRange(ctx, householdID int64, from, to string) ([]*Plan, error)` — date-bounded slice for stats
- `GetPlanByWeekStart(ctx, householdID int64, weekStart string) (*Plan, error)` — calendar navigation

```
db: add plan history and range queries
```
Status: ⬜

---

### 2. db: add spend stats query
Files: `db/stats.go` (new), `db/models.go`, `db/store.go`

New model:
```go
type SpendStats struct {
    TotalCents  int64
    BudgetCents int64
    MealCount   int64
    PlanCount   int
}
```

New interface method + implementation:
- `GetSpendStats(ctx, householdID int64, from, to string) (*SpendStats, error)`
  — sums `total_cents` and `budget_cents`, counts meals and plans over the
  date range. Plans with `status = 'generating'` excluded from totals.

```
db: add spend stats query
```
Status: ⬜

---

### 3. config: add WEEK_START_DAY and canonical week-bounds helper
Files: `config/config.go`, `plan/week.go` (new)

- `Config.WeekStartDay string` — read from env `WEEK_START_DAY`, default `"sunday"`.
  Accepted values: `"sunday"` | `"monday"`.
- New file `plan/week.go`:
  ```go
  // WeekBounds returns the Monday-or-Sunday-anchored week that contains t.
  func WeekBounds(t time.Time, startDay string) (start, end time.Time)
  ```
  All week-boundary math in the app reads this one function. No other package
  may call `AddDate` or `Weekday()` arithmetic to compute a week boundary.

```
config: add WEEK_START_DAY setting and canonical WeekBounds helper
```
Status: ⬜

---

### 4. web: dashboard handler with spend stats and plan history
Files: `web/handlers.go`

Replace `s.render(w, r, "index", nil)` with a real load:

```go
type dashPageData struct {
    HasPlan        bool
    PlanStatus     string   // "generating" | "ready" | ""
    WeekLabel      string   // "Sep 1 – Sep 7"
    TotalLabel     string   // "$47.20" or ""
    BudgetLabel    string   // "$120"
    OverBudget     bool
    Stats          *db.SpendStats
    RecentPlans    []*db.Plan  // last 8, for history strip
    HasLLM         bool
}
```

Handler loads: latest plan, current-week spend stats (via `WeekBounds`),
last 8 plans for the history strip. All DB errors are soft — a nil value
renders the empty state instead of a 500.

```
web: dashboard handler with spend stats and plan history
```
Status: ⬜

---

### 5. web: dashboard template — stats, history, plan CTA
Files: `web/templates/index.html`

Replace the build-progress placeholder with a real dashboard layout:

**Plan status card** (top):
- If generating: spinner + "Generating your plan…" + link to `/plan/generate`
- If ready: budget meter (same token family as `/plan`), total vs budget label,
  "View plan" link, "Regenerate" button (if LLM configured)
- If none: "Plan My Week" CTA button + onboarding copy

**This-week stats strip** (below card, 3 chips):
- Total spent / budget (badge--success if under, badge--warning if within 10%,
  badge--danger if over)
- Meals planned this week
- Plans generated all time

**Plan history table** (`data-table="plan-history"`):
- Columns: Week, Meals, Spent, Budget, Δ (over/under badge)
- Each week-start is a link to `/plan?week=YYYY-MM-DD`
- Empty state: "No past plans yet — generate your first plan above."

```
web: dashboard template — stats, history, plan CTA
```
Status: ⬜

---

### 6. web: past-plan calendar view and history page
Files: `web/handlers_plan.go`, `web/templates/history.html` (new),
`web/routes.go`

**`GET /plan?week=YYYY-MM-DD`** (extend `handlePlanPage`):
- If `week` query param present, call `GetPlanByWeekStart` instead of
  `GetLatestPlan`. Render the same `plan.html` calendar in read-only mode:
  no headcount form, no lock/feedback forms, a "← Back" breadcrumb.
- `planPageData` gains `ReadOnly bool`; template gates the action controls
  on `{{if not .Data.ReadOnly}}`.

**`GET /plan/history`** (`handlePlanHistory`, new):
- Loads all plans via `ListPlans`. Accepts `?from=&to=` date filter
  (`ListPlansInRange` when both present); params reflected in URL.
- Renders `history.html`: full-width `data-table="plan-history"` with the
  same columns as the dashboard strip plus a "View" link per row.

Routes added:
```
GET /plan/history
```
(`GET /plan` already registered; `?week=` is a query param, not a new route.)

```
web: plan history page and read-only past-plan calendar view
```
Status: ⬜

---

### 7. spec: mark Phase 5.5 complete
Files: `spec/commit-plan-phase5.5-09-05-26.md` (this file, all ✅)

```
spec: mark all Phase 5.5 commits complete
```
Status: ⬜

---

## Summary

7 commits covering: plan history DB queries, spend stats, week-boundary
config, dashboard handler, dashboard template, past-plan read-only calendar
view, and the history page. All reads from stored `total_cents` — no
re-pricing, stable history.
