package db

import "context"

func (s *store) GetSpendStats(ctx context.Context, householdID int64, from, to string) (*SpendStats, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(p.total_cents), 0),
			COALESCE(SUM(p.budget_cents), 0),
			COALESCE((
				SELECT COUNT(*) FROM meals m
				WHERE m.plan_id IN (
					SELECT id FROM plans
					WHERE household_id = ?1
					  AND week_start >= ?2
					  AND week_start <= ?3
					  AND status = 'ready'
				)
			), 0),
			COUNT(*)
		FROM plans p
		WHERE p.household_id = ?1
		  AND p.week_start >= ?2
		  AND p.week_start <= ?3
		  AND p.status = 'ready'`,
		householdID, from, to,
	)

	var st SpendStats
	if err := row.Scan(&st.TotalCents, &st.BudgetCents, &st.MealCount, &st.PlanCount); err != nil {
		return &SpendStats{}, nil
	}
	return &st, nil
}
