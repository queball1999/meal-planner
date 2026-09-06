package plan

import (
	"context"
	"fmt"
	"sort"

	"goeat/db"
)

var slotOrder = map[string]int{
	"breakfast": 0,
	"lunch":     1,
	"dinner":    2,
}

// PlanLeftovers marks downstream meal slots as leftovers when an earlier meal
// produces more cooked portions than it needs (§5.6). No-op when tolerance is false.
func PlanLeftovers(ctx context.Context, store db.Store, planID int64, tolerance bool) error {
	if !tolerance {
		return nil
	}

	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil {
		return fmt.Errorf("leftovers: list meals: %w", err)
	}

	// Walk meals in chronological order (date, then B/L/D).
	sort.Slice(meals, func(i, j int) bool {
		if meals[i].Day != meals[j].Day {
			return meals[i].Day < meals[j].Day
		}
		return slotOrder[meals[i].Slot] < slotOrder[meals[j].Slot]
	})

	type surplusEntry struct {
		sourceMealID int64
		portions     int
	}
	var pool []surplusEntry

	for _, m := range meals {
		if m.IsLeftover {
			// Already flagged; consume from the pool.
			if len(pool) > 0 {
				pool[0].portions -= m.Servings
				if pool[0].portions <= 0 {
					pool = pool[1:]
				}
			}
			continue
		}

		// Can this slot be covered by leftover surplus?
		if len(pool) > 0 && pool[0].portions >= m.Servings {
			srcID := pool[0].sourceMealID
			pool[0].portions -= m.Servings
			if pool[0].portions <= 0 {
				pool = pool[1:]
			}
			if err := store.UpdateMealLeftover(ctx, m.ID, true, &srcID); err != nil {
				return fmt.Errorf("leftovers: mark meal %d: %w", m.ID, err)
			}
			continue
		}

		// Accumulate this meal's surplus for downstream slots.
		if m.CookedPortions > m.Servings {
			pool = append(pool, surplusEntry{
				sourceMealID: m.ID,
				portions:     m.CookedPortions - m.Servings,
			})
		}
	}

	return nil
}
