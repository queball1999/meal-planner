package db

import "context"

// ShoppingSpend holds the spending breakdown for a plan's shopping list.
type ShoppingSpend struct {
	EstimatedCents int64 // sum of all non-InPantry line totals (what we plan to spend)
	ActualCents    int64 // sum of checked line totals (what we've actually bought)
	PendingCents   int64 // sum of pending (unpriced) line totals
	PendingCount   int   // number of pending items
}

// GetShoppingListSpend returns the actual vs estimated spending breakdown for
// a plan's shopping list. It sums line_total_cents from shopping_list_items:
//   - Estimated = all non-InPantry items (what we plan to buy)
//   - Actual    = checked items only (what we've actually bought)
//   - Pending   = unpriced items (still being resolved)
//
// A line marked InPantry ("I already have this") is excluded from all totals
// because it's not being bought. Pending items are excluded from Actual so
// they don't inflate the "already spent" number before a price resolves.
func (s *store) GetShoppingListSpend(ctx context.Context, planID int64) (*ShoppingSpend, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN in_pantry = 0 THEN line_total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN in_pantry = 0 AND checked = 1 AND pending = 0 THEN line_total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pending = 1 THEN line_total_cents ELSE 0 END), 0),
			COUNT(CASE WHEN pending = 1 THEN 1 END)
		FROM shopping_list_items
		WHERE plan_id = ?`,
		planID,
	)

	var sp ShoppingSpend
	if err := row.Scan(&sp.EstimatedCents, &sp.ActualCents, &sp.PendingCents, &sp.PendingCount); err != nil {
		return &sp, nil
	}
	return &sp, nil
}
