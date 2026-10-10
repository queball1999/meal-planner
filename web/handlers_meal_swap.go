package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"goeat/db"
	"goeat/middleware"
	"goeat/plan"
)

// handleMealSwap replaces one meal with a saved recipe, in the same slot,
// scaled to what the day is feeding - the meal card's swap icon. Leftover
// meals living off the old one are resolved the way the picker's dialog said
// (plan.SwapMeal), mirroring handleMealStatus.
//
//	POST /meals/{id}/swap  {recipe_id, resolution}
func (s *Server) handleMealSwap(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	meal, p := s.loadOwnedMeal(r, hh, id)
	if meal == nil {
		http.NotFound(w, r)
		return
	}
	recipeID, err := strconv.ParseInt(r.FormValue("recipe_id"), 10, 64)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Pick a recipe first.")
		http.Redirect(w, r, planURL(p), http.StatusSeeOther)
		return
	}
	resolution := r.FormValue("resolution")
	if !plan.ValidSwapResolution(resolution) {
		s.setNotify(w, NotifyDanger, "That isn't a leftover option.")
		http.Redirect(w, r, planURL(p), http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	portions := float64(hh.HouseholdSize)
	if day, _ := s.store.GetPlanDay(ctx, p.ID, meal.Day); day != nil && day.Portions > 0 {
		portions = day.Portions
	}

	res, err := plan.SwapMeal(ctx, s.store, plan.SwapParams{
		MealID:          meal.ID,
		HouseholdID:     hh.ID,
		CatalogRecipeID: recipeID,
		Portions:        portions,
		Resolution:      resolution,
	})
	if err != nil {
		log.Printf("meal swap %d: %v", meal.ID, err)
		s.setNotify(w, NotifyDanger, "Couldn't swap that meal.")
		http.Redirect(w, r, planURL(p), http.StatusSeeOther)
		return
	}

	// Same as handleMealFill: a slot someone just picked a recipe for is being
	// cooked, so an eating-out day would otherwise keep it off the list.
	if err := s.store.SetPlanDayStatus(ctx, p.ID, meal.Day, db.DayCooking); err != nil {
		log.Printf("meal swap: reset day status %s: %v", meal.Day, err)
	}

	// New meal content, so a full reprice like generation and fill.
	s.repriceInBackground(p.ID, hh, false)
	s.setNotify(w, NotifySuccess, mealSwapMessage(res, dayLabel(meal.Day)+" "+meal.Slot, resolution))

	dest := planURL(p)
	if res.FillDate != "" {
		dest += "&fill=" + url.QueryEscape(res.FillDate+"|"+res.FillSlot)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func mealSwapMessage(res *plan.SwapResult, mealLabel, resolution string) string {
	base := fmt.Sprintf("%s swapped from %s to %s, scaled to %d servings.",
		mealLabel, res.OldTitle, res.Title, res.Servings)
	switch {
	case res.Resolved == 0:
	case resolution == plan.SwapReplace:
		return base + fmt.Sprintf(" %d leftover meal(s) cleared. Pick something for the first slot.", res.Resolved)
	case resolution == plan.SwapClear:
		base += fmt.Sprintf(" %d leftover meal(s) cleared.", res.Resolved)
	case resolution == plan.SwapIgnore:
		base += fmt.Sprintf(" %d leftover meal(s) left as they were.", res.Resolved)
	default:
		base += fmt.Sprintf(" It now cooks extra for %d leftover meal(s).", res.Resolved)
	}
	return base + " The shopping list is updating."
}
