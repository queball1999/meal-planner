package plan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"goeat/db"
)

var slotOrder = map[string]int{
	"breakfast": 0,
	"lunch":     1,
	"dinner":    2,
}

// leftoverMaxDayGap is how many days a batch-cooked surplus stays edible: a
// meal is only flagged as living off an earlier one when they are on the same
// day or consecutive days. Spec §4.3 is "the next day's lunch or dinner", and
// without this the greedy matcher below would tag, say, Friday's oatmeal as
// "leftovers from Monday's chili" purely because the portion count lined up -
// which is where "the recycle icon is on meals that aren't leftovers" came
// from.
const leftoverMaxDayGap = 1

// PlanLeftovers marks downstream meal slots as leftovers when an earlier meal
// produces more cooked portions than it needs (§5.6). No-op when tolerance is
// false.
//
// A slot is only eligible to be a leftover target when it is lunch or dinner
// (nobody plans last night's stir-fry for breakfast) and within
// leftoverMaxDayGap days of the meal that cooked the surplus.
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
		day          time.Time
		portions     int
	}
	var pool []surplusEntry

	// dropStale removes surplus entries too old to still be eaten by a meal on
	// `day`, so a Monday batch-cook cannot reach across the whole week.
	dropStale := func(day time.Time) {
		kept := pool[:0]
		for _, e := range pool {
			if day.Sub(e.day) <= leftoverMaxDayGap*24*time.Hour {
				kept = append(kept, e)
			}
		}
		pool = kept
	}

	for _, m := range meals {
		day, derr := time.Parse("2006-01-02", m.Day)
		if derr != nil {
			// A meal with an unparseable day cannot be placed on the timeline;
			// leave it alone rather than mis-linking it.
			continue
		}
		dropStale(day)

		if m.IsLeftover {
			// Already flagged (a re-run over a plan that was matched before);
			// consume from the pool so later slots see the right remainder.
			if len(pool) > 0 {
				pool[0].portions -= m.Servings
				if pool[0].portions <= 0 {
					pool = pool[1:]
				}
			}
			continue
		}

		// Can this slot be covered by leftover surplus? Breakfast never is.
		if m.Slot != "breakfast" && len(pool) > 0 && pool[0].portions >= m.Servings {
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
				day:          day,
				portions:     m.CookedPortions - m.Servings,
			})
		}
	}

	return nil
}
