package web

import (
	"fmt"
	"net/http"
	"strconv"

	"goeat/db"
	"goeat/middleware"
)

// loadOwnedMeal returns the meal for id and the plan it belongs to, or nil
// when either doesn't exist or the plan belongs to a different household -
// the same scoping handleMealCard uses for the hover preview.
func (s *Server) loadOwnedMeal(r *http.Request, hh *db.Household, id int64) (*db.Meal, *db.Plan) {
	ctx := r.Context()
	meal, err := s.store.GetMealByID(ctx, id)
	if err != nil || meal == nil {
		return nil, nil
	}
	plan, err := s.store.GetPlanByID(ctx, meal.PlanID)
	if err != nil || plan == nil || plan.HouseholdID != hh.ID {
		return nil, nil
	}
	return meal, plan
}

// handleMealStatusImpact answers "what breaks if I mark this one meal?"
// before anything changes - the per-meal counterpart of
// handleDayStatusImpact, narrowed to the meals that eat *this* meal's
// leftovers rather than every leftover on its day (ListLeftoversSourcedFromMeal).
//
//	GET /meals/{id}/status-impact
func (s *Server) handleMealStatusImpact(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad id"})
		return
	}
	meal, _ := s.loadOwnedMeal(r, hh, id)
	if meal == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "meal not found"})
		return
	}

	orphans, err := s.store.ListLeftoversSourcedFromMeal(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't check that"})
		return
	}

	out := dayStatusImpact{OK: true, Date: meal.Day, Orphaned: []orphanedMeal{}}
	for _, m := range orphans {
		out.Orphaned = append(out.Orphaned, orphanedMeal{
			MealID:    m.ID,
			Title:     m.Title,
			Date:      m.Day,
			DateLabel: dayLabel(m.Day),
			Slot:      m.Slot,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         out.OK,
		"orphaned":   out.Orphaned,
		"meal_id":    meal.ID,
		"meal_label": dayLabel(meal.Day) + " " + meal.Slot,
	})
}

// handleMealStatus sets one meal's own cooking status (skip / eating out /
// back to cooking) and applies the chosen resolution for any leftovers that
// were living off it - the meal-card icons' counterpart of handleDayStatus.
// Resolutions are the same four (cascade/clear/replace/ignore), but "cascade"
// marks the dependent *meals* the same way rather than their whole days.
//
//	POST /meals/{id}/status  {status, resolution}
func (s *Server) handleMealStatus(w http.ResponseWriter, r *http.Request) {
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
	meal, plan := s.loadOwnedMeal(r, hh, id)
	if meal == nil {
		http.NotFound(w, r)
		return
	}
	status := r.FormValue("status")
	if !db.ValidDayStatus(status) {
		s.setNotify(w, NotifyDanger, "That isn't a meal status.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	ctx := r.Context()

	// Read the dependents before changing anything: once the status is set, a
	// cascade would look at a meal that is already marked.
	var orphans []*db.Meal
	if status != db.DayCooking {
		orphans, _ = s.store.ListLeftoversSourcedFromMeal(ctx, id)
	}

	if err := s.store.SetMealStatus(ctx, id, status); err != nil {
		s.setNotify(w, NotifyDanger, "Couldn't save that. Try again.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	resolved := 0
	resolution := r.FormValue("resolution")
	var fillDate, fillSlot string
	if resolution == "replace" && len(orphans) > 0 {
		fillDate, fillSlot = orphans[0].Day, orphans[0].Slot
	}

	switch resolution {
	case "cascade":
		for _, m := range orphans {
			if err := s.store.SetMealStatus(ctx, m.ID, status); err != nil {
				continue
			}
			resolved++
		}
	case "clear", "replace":
		for _, m := range orphans {
			if err := s.store.DeleteMeal(ctx, m.ID); err != nil {
				continue
			}
			resolved++
		}
	}

	// Quantity-only: skipping/restoring a meal never adds new meal content,
	// only drops or restores ingredients the list is already pricing.
	s.repriceInBackground(plan.ID, hh, true)

	mealLabel := dayLabel(meal.Day) + " " + meal.Slot
	s.setNotify(w, NotifySuccess, mealStatusMessage(mealLabel, status, resolved, resolution))

	dest := "/plan"
	if fillDate != "" {
		dest += fmt.Sprintf("?fill=%s|%s", fillDate, fillSlot)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func mealStatusMessage(mealLabel, status string, resolved int, resolution string) string {
	label := dayStatusLabel[status]
	base := fmt.Sprintf("%s marked %s. The shopping list is updating.", mealLabel, label)
	if resolved == 0 {
		return base
	}
	switch resolution {
	case "cascade":
		return fmt.Sprintf("%s marked %s, along with %d meal(s) that were living off it. The shopping list is updating.",
			mealLabel, label, resolved)
	case "replace":
		return fmt.Sprintf("%s marked %s and %d leftover meal(s) cleared. Pick something for the first slot.",
			mealLabel, label, resolved)
	case "clear":
		return fmt.Sprintf("%s marked %s and %d leftover meal(s) cleared. The shopping list is updating.",
			mealLabel, label, resolved)
	}
	return base
}
