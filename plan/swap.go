package plan

import (
	"context"
	"fmt"
	"log"

	"goeat/db"
)

// What happens to later meals that were eating the swapped-out meal's
// leftovers. The skip / eating-out flow's "clear", "replace" and "ignore"
// mean the same here; "carry" replaces its "cascade" (marking dependents the
// same way has no meaning for a swap).
const (
	SwapCarry   = "carry"   // new meal cooks extra; dependents become its leftovers
	SwapClear   = "clear"   // delete the dependents, leave their slots empty
	SwapReplace = "replace" // delete them; the caller opens a picker for the first
	SwapIgnore  = "ignore"  // keep them as leftovers with no source meal
)

// SwapParams says which meal is replaced by which saved recipe.
type SwapParams struct {
	MealID          int64
	HouseholdID     int64
	CatalogRecipeID int64
	Portions        float64 // the day's portion total, as for MaterializeParams
	Resolution      string  // one of the Swap* constants; "" means SwapCarry
}

// SwapResult reports the new meal and what happened to its dependents.
type SwapResult struct {
	*MaterializeResult
	OldTitle string
	Resolved int // dependents carried over, cleared or kept
	// FillDate/FillSlot name the first cleared slot for SwapReplace, so the
	// caller can open a picker for it.
	FillDate, FillSlot string
}

// SwapMeal replaces one meal with a saved recipe, in the same slot, and
// resolves any later meals that were eating its leftovers.
//
// Dependents are read before anything changes: MaterializeRecipe deletes the
// old meal, and DeleteMeal unhooks every leftover that pointed at it.
func SwapMeal(ctx context.Context, store db.Store, p SwapParams) (*SwapResult, error) {
	old, err := store.GetMealByID(ctx, p.MealID)
	if err != nil {
		return nil, fmt.Errorf("get meal: %w", err)
	}
	if old == nil {
		return nil, fmt.Errorf("meal %d not found", p.MealID)
	}
	orphans, err := store.ListLeftoversSourcedFromMeal(ctx, p.MealID)
	if err != nil {
		return nil, fmt.Errorf("list leftovers: %w", err)
	}

	resolution := p.Resolution
	if resolution == "" {
		resolution = SwapCarry
	}

	var extra float64
	if resolution == SwapCarry {
		for _, m := range orphans {
			extra += float64(m.Servings)
		}
	}

	res, err := MaterializeRecipe(ctx, store, MaterializeParams{
		PlanID:          old.PlanID,
		HouseholdID:     p.HouseholdID,
		CatalogRecipeID: p.CatalogRecipeID,
		Date:            old.Day,
		Slot:            old.Slot,
		Portions:        p.Portions,
		ExtraPortions:   extra,
	})
	if err != nil {
		return nil, err
	}

	out := &SwapResult{MaterializeResult: res, OldTitle: old.Title}

	switch resolution {
	case SwapCarry:
		newID := res.MealID
		for _, m := range orphans {
			if err := store.UpdateMealLeftover(ctx, m.ID, true, &newID); err != nil {
				log.Printf("swap: relink leftover meal %d: %v", m.ID, err)
				continue
			}
			// Keep the title in step with what is actually in the fridge, and
			// "leftover" in it: db.IsLeftoverTitle is how the rest of the app
			// recognises a leftover meal.
			if err := store.UpdateMealTitle(ctx, m.ID, "Leftover "+res.Title, m.Effort, m.Servings, m.CookedPortions); err != nil {
				log.Printf("swap: retitle leftover meal %d: %v", m.ID, err)
			}
			out.Resolved++
		}
	case SwapClear, SwapReplace:
		for _, m := range orphans {
			if err := store.DeleteMeal(ctx, m.ID); err != nil {
				log.Printf("swap: clear leftover meal %d: %v", m.ID, err)
				continue
			}
			if out.FillDate == "" && resolution == SwapReplace {
				out.FillDate, out.FillSlot = m.Day, m.Slot
			}
			out.Resolved++
		}
	case SwapIgnore:
		// Still leftovers (so they stay off the shopping list), just no
		// longer tied to a meal - the state PlanLeftovers leaves a titled
		// leftover in when it finds no surplus to point at.
		for _, m := range orphans {
			if err := store.UpdateMealLeftover(ctx, m.ID, true, nil); err != nil {
				log.Printf("swap: keep leftover meal %d: %v", m.ID, err)
				continue
			}
			out.Resolved++
		}
	}
	return out, nil
}

// ValidSwapResolution reports whether r is a resolution SwapMeal accepts.
func ValidSwapResolution(r string) bool {
	switch r {
	case "", SwapCarry, SwapClear, SwapReplace, SwapIgnore:
		return true
	}
	return false
}
