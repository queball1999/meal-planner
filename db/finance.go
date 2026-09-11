package db

import (
	"context"
	"time"
)

// ── Finance dashboard aggregations ────────────────────────────────────────────
//
// "Actual" spend is what has been bought: shopping-list lines checked off,
// excluding "already in the pantry" (not bought) and still-pending (no price).
// "Estimate" is the full non-pantry list total. Only non-canceled plans count -
// a regenerate supersedes the prior week's plan, and summing both would double
// count the same trip.

// FinanceWeek is one week's actual vs estimated spend (the week-over-week chart).
type FinanceWeek struct {
	WeekStart     string // YYYY-MM-DD
	ActualCents   int64
	EstimateCents int64
	BudgetCents   int64
}

// FinanceTopItem is one item's spend rollup (the "top items" table).
type FinanceTopItem struct {
	DisplayName   string
	ActualCents   int64
	EstimateCents int64
}

// FinanceStoreSpend is one store's spend rollup (the "by store" table).
type FinanceStoreSpend struct {
	StoreName     string
	ActualCents   int64
	EstimateCents int64
}

const financeRange = `p.household_id = ? AND p.canceled = 0 AND p.week_start >= ? AND p.week_start <= ?`

// GetWeeklySpend returns actual vs estimated spend per week in the range,
// oldest first. Weeks with no plan are simply absent - the handler fills the
// gaps so the chart shows a continuous axis.
func (s *store) GetWeeklySpend(ctx context.Context, householdID int64, from, to string) ([]*FinanceWeek, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.week_start,
			COALESCE(SUM(CASE WHEN sli.in_pantry = 0 THEN sli.line_total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN sli.in_pantry = 0 AND sli.checked = 1 AND sli.pending = 0 THEN sli.line_total_cents ELSE 0 END), 0),
			MAX(p.budget_cents)
		FROM plans p
		LEFT JOIN shopping_list_items sli ON sli.plan_id = p.id
		WHERE `+financeRange+`
		GROUP BY p.week_start
		ORDER BY p.week_start ASC`,
		householdID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*FinanceWeek
	for rows.Next() {
		var w FinanceWeek
		if err := rows.Scan(&w.WeekStart, &w.EstimateCents, &w.ActualCents, &w.BudgetCents); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// GetTopSpentItems returns the items with the highest actual spend in the range.
func (s *store) GetTopSpentItems(ctx context.Context, householdID int64, from, to string, limit int) ([]*FinanceTopItem, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sli.display_name,
			COALESCE(SUM(CASE WHEN sli.checked = 1 AND sli.in_pantry = 0 AND sli.pending = 0 THEN sli.line_total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN sli.in_pantry = 0 THEN sli.line_total_cents ELSE 0 END), 0)
		FROM shopping_list_items sli
		JOIN plans p ON p.id = sli.plan_id
		WHERE `+financeRange+`
		GROUP BY sli.display_name
		ORDER BY 2 DESC, sli.display_name ASC
		LIMIT ?`,
		householdID, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*FinanceTopItem
	for rows.Next() {
		var r FinanceTopItem
		if err := rows.Scan(&r.DisplayName, &r.ActualCents, &r.EstimateCents); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// GetStoreSpend returns spend by store in the range (unassigned items roll up
// under "Other").
func (s *store) GetStoreSpend(ctx context.Context, householdID int64, from, to string) ([]*FinanceStoreSpend, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(st.name, 'Other'),
			COALESCE(SUM(CASE WHEN sli.in_pantry = 0 THEN sli.line_total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN sli.in_pantry = 0 AND sli.checked = 1 AND sli.pending = 0 THEN sli.line_total_cents ELSE 0 END), 0)
		FROM shopping_list_items sli
		JOIN plans p ON p.id = sli.plan_id
		LEFT JOIN stores st ON st.id = sli.store_id
		WHERE `+financeRange+`
		GROUP BY COALESCE(st.name, 'Other')
		ORDER BY 2 DESC, 1 ASC`,
		householdID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*FinanceStoreSpend
	for rows.Next() {
		var r FinanceStoreSpend
		if err := rows.Scan(&r.StoreName, &r.EstimateCents, &r.ActualCents); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// GetFinanceTotals aggregates actual/estimated spend and budget over the range
// (the summary strip).
func (s *store) GetFinanceTotals(ctx context.Context, householdID int64, from, to string) (actual, estimate, budget int64, err error) {
	// Budget is summed over plans (not items) in a subquery - joining plans to
	// their items would repeat each budget once per line and inflate the total.
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE((
				SELECT SUM(p.budget_cents) FROM plans p
				WHERE p.household_id = ? AND p.canceled = 0
				  AND p.week_start >= ? AND p.week_start <= ?
			), 0),
			COALESCE((
				SELECT SUM(sli.line_total_cents)
				FROM shopping_list_items sli
				JOIN plans p ON p.id = sli.plan_id
				WHERE p.household_id = ? AND p.canceled = 0
				  AND p.week_start >= ? AND p.week_start <= ?
				  AND sli.in_pantry = 0
			), 0),
			COALESCE((
				SELECT SUM(sli.line_total_cents)
				FROM shopping_list_items sli
				JOIN plans p ON p.id = sli.plan_id
				WHERE p.household_id = ? AND p.canceled = 0
				  AND p.week_start >= ? AND p.week_start <= ?
				  AND sli.in_pantry = 0 AND sli.checked = 1 AND sli.pending = 0
			), 0)`,
		householdID, from, to,
		householdID, from, to,
		householdID, from, to,
	)
	if err := row.Scan(&budget, &estimate, &actual); err != nil {
		return 0, 0, 0, nil
	}
	return actual, estimate, budget, nil
}

// FinanceWeekLabel formats a week start as a short "Sep 1" label.
func FinanceWeekLabel(weekStart string) string {
	t, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return weekStart
	}
	return t.Format("Jan 2")
}
