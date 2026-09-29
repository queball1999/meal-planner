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
// meal is only linked to an earlier one when they are on the same day or
// consecutive days. Spec §4.3 is "the next day's lunch or dinner".
const leftoverMaxDayGap = 1

// PlanLeftovers ties each meal the model titled as leftovers back to the
// nearest earlier meal that actually cooked a surplus (§5.6), so the "this
// meal feeds another one" UI has a real parent to point at.
//
// The title (db.IsLeftoverTitle) is the only source of truth for *which*
// meals are leftovers - the prompt (BuildPrompt, §4.6) asks the model to
// "note in the title when a meal is intentional leftovers". Matching on
// cooked_portions vs servings alone was marking every next-day lunch as a
// leftover the moment any earlier meal had a couple of spare portions, which
// is what a household batch-cooking dinner "just in case" does routinely. No-op when
// tolerance is false. Breakfast is never eligible, matching the prompt (nobody
// plans last night's stir-fry for breakfast) even if the model's title
// suggests otherwise.
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

	// The week's first day cooks nothing else to have leftovers from - the
	// plan starts fresh. Guards against the model titling a first-day meal
	// as leftovers anyway (BuildPrompt tells it not to; this is the backstop).
	var firstDay string
	if len(meals) > 0 {
		firstDay = meals[0].Day
	}

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

		wantsLeftover := m.Slot != "breakfast" && m.Day != firstDay && db.IsLeftoverTitle(m.Title)

		if wantsLeftover {
			// Tie it to the nearest still-warm surplus, if one exists. The
			// model's own portion math does not always line up exactly with
			// its title, so a titled leftover meal is still marked even when
			// no matching surplus can be found - the title is authoritative;
			// the source link is best-effort on top of it.
			var srcID *int64
			if len(pool) > 0 {
				id := pool[0].sourceMealID
				srcID = &id
				pool[0].portions -= m.Servings
				if pool[0].portions <= 0 {
					pool = pool[1:]
				}
			}
			if !m.IsLeftover || m.LeftoverSourceMealID == nil || srcID == nil || *m.LeftoverSourceMealID != *srcID {
				if err := store.UpdateMealLeftover(ctx, m.ID, true, srcID); err != nil {
					return fmt.Errorf("leftovers: mark meal %d: %w", m.ID, err)
				}
			}
			continue
		}

		// Not titled as leftovers - clear a stale flag a previous, looser
		// version of this heuristic may have left behind.
		if m.IsLeftover {
			if err := store.UpdateMealLeftover(ctx, m.ID, false, nil); err != nil {
				return fmt.Errorf("leftovers: clear stale flag on meal %d: %w", m.ID, err)
			}
		}

		// This meal's own surplus, if any, is available to a later titled
		// leftover meal.
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
