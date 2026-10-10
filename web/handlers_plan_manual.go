package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"goeat/db"
	"goeat/middleware"
	"goeat/plan"
)

// planForDate returns the household's active plan covering date - the plan an
// edit to that day (who's eating, a slot fill, a day status) belongs to.
//
// By the date rather than "the latest plan": with next week planned ahead the
// newest plan is next week's, and an edit made on this week's board would
// otherwise land on a plan that has no such day. The latest plan is still the
// fallback when it covers the date, for a plan whose week_start predates a
// WEEK_START_DAY change and so no longer lines up with plan.WeekBounds.
func (s *Server) planForDate(ctx context.Context, hh *db.Household, date string) *db.Plan {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	weekStart, _ := plan.WeekBounds(day, s.cfg.WeekStartDay)
	if p, _ := s.store.GetPlanByWeekStart(ctx, hh.ID, weekStart.Format("2006-01-02")); p != nil {
		return p
	}
	if p, _ := s.store.GetLatestPlan(ctx, hh.ID); p != nil && p.WeekStart <= date && date <= p.WeekEnd {
		return p
	}
	return nil
}

// planURL is the plan board for p's own week - where an edit redirects back
// to, so the board that reloads is the one that was just edited rather than
// whichever plan happens to be newest.
func planURL(p *db.Plan) string {
	if p == nil {
		return "/plan"
	}
	return "/plan?week=" + p.WeekStart
}

// rescaleDay re-applies a day's saved portions to its meals. A meal moved in
// from a day feeding a different number of people arrives at its old day's
// amounts; this is what brings it to the new day's.
func (s *Server) rescaleDay(ctx context.Context, planID int64, date string) {
	day, err := s.store.GetPlanDay(ctx, planID, date)
	if err != nil || day == nil || day.Portions < 1 {
		return
	}
	in := dayPortionInput{
		Portions:       day.Portions,
		MemberPortions: day.Portions - float64(day.Guests),
		Guests:         day.Guests,
		GuestSlots:     day.GuestSlots,
	}
	if _, err := s.scaleDayForInput(ctx, planID, date, in); err != nil {
		log.Printf("rescale day %s of plan %d: %v", date, planID, err)
	}
}

// handlePlanManual starts a plan for a week without the LLM: an empty board
// whose slots are filled one at a time from the household's saved recipes
// (handleMealFill). A week that already has a plan just opens it.
//
//	POST /plan/manual  {week}
func (s *Server) handlePlanManual(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	// Same week rules as generating one: "" is the week someone planning now
	// wants, and a week that has ended is refused.
	target, err := s.resolveGenerateTarget(r.FormValue("week"), "", time.Now())
	switch {
	case errors.Is(err, errGeneratePastWeek):
		s.setNotify(w, NotifyDanger, "Past weeks can't be planned - they're kept in Plan History for reference.")
		http.Redirect(w, r, "/plan?week="+target.WeekStart.Format("2006-01-02"), http.StatusSeeOther)
		return
	case err != nil:
		s.setNotify(w, NotifyDanger, "That isn't a valid week.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	p, created, err := plan.CreateManual(r.Context(), s.store, hh.ID, target.WeekStart)
	if err != nil {
		log.Printf("manual plan household=%d week=%s: %v", hh.ID, target.WeekStart.Format("2006-01-02"), err)
		s.setNotify(w, NotifyDanger, "Couldn't start that plan. Try again.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}
	week := fmtMonthDay(p.WeekStart) + " - " + fmtMonthDay(p.WeekEnd)
	if created {
		s.setNotify(w, NotifySuccess, "Started a plan for "+week+". Add a meal to any slot.")
	} else {
		s.setNotify(w, NotifyInfo, week+" already has a plan - add to it or move its meals around here.")
	}
	http.Redirect(w, r, planURL(p), http.StatusSeeOther)
}

// leftoverSource is one earlier meal a slot could eat the leftovers of.
type leftoverSource struct {
	MealID   int64  `json:"meal_id"`
	Title    string `json:"title"`
	Label    string `json:"label"` // "Mon Oct 12 dinner"
	Servings int    `json:"servings"`
}

// handleLeftoverSources lists the meals a slot could be the leftovers of, for
// the recipe picker's "use leftovers" section.
//
//	GET /plan/days/{date}/{slot}/leftover-sources
func (s *Server) handleLeftoverSources(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	date, slot := r.PathValue("date"), r.PathValue("slot")
	if !validSlot(slot) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad slot"})
		return
	}
	ctx := r.Context()
	out := []leftoverSource{}
	if p := s.planForDate(ctx, hh, date); p != nil {
		meals, err := s.store.ListMealsByPlan(ctx, p.ID)
		if err != nil {
			log.Printf("leftover sources %s %s: %v", date, slot, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't load meals"})
			return
		}
		for _, m := range plan.LeftoverSources(meals, date, slot) {
			out = append(out, leftoverSource{
				MealID: m.ID, Title: m.Title,
				Label: dayLabel(m.Day) + " " + m.Slot, Servings: m.Servings,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sources": out})
}

// moveTarget reads and checks the {date, slot} a move is aimed at: a real
// slot, on a day inside the meal's own plan.
func moveTarget(p *db.Plan, date, slot string) bool {
	if !validSlot(slot) {
		return false
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return false
	}
	return p.WeekStart <= date && date <= p.WeekEnd
}

// handleMealMoveImpact answers "what would moving this meal there disturb?"
// before anything changes - the meal already in that slot, and any leftover
// meals the move would strand - so the board can ask rather than guess.
//
//	GET /meals/{id}/move-impact?date=&slot=&occupied=
func (s *Server) handleMealMoveImpact(w http.ResponseWriter, r *http.Request) {
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
	meal, p := s.loadOwnedMeal(r, hh, id)
	if meal == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "meal not found"})
		return
	}
	q := r.URL.Query()
	date, slot := q.Get("date"), q.Get("slot")
	if !moveTarget(p, date, slot) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "that slot isn't on this plan"})
		return
	}

	meals, err := s.store.ListMealsByPlan(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't check that"})
		return
	}
	// Both outcomes of an occupied slot, so the dialog can say what each
	// choice strands without a second round trip.
	stranded := func(occupied string) []orphanedMeal {
		out := []orphanedMeal{}
		impact, err := plan.PlanMove(meals, id, date, slot, occupied)
		if err != nil {
			return out
		}
		for _, m := range impact.Stranded {
			out = append(out, orphanedMeal{
				MealID: m.ID, Title: m.Title, Date: m.Day, DateLabel: dayLabel(m.Day), Slot: m.Slot,
			})
		}
		return out
	}
	impact, _ := plan.PlanMove(meals, id, date, slot, plan.MoveSwap)

	resp := map[string]any{
		"ok":               true,
		"meal_label":       dayLabel(meal.Day) + " " + meal.Slot,
		"target_label":     dayLabel(date) + " " + slot,
		"stranded_swap":    stranded(plan.MoveSwap),
		"stranded_replace": stranded(plan.MoveReplace),
	}
	if d := impact.Displaced; d != nil {
		resp["displaced"] = map[string]any{"meal_id": d.ID, "title": d.Title, "locked": d.Locked}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleMealMove relocates a meal to another slot of its plan - the plan
// board's drag-and-drop, and its "Move" menu item for a touch screen or a
// keyboard. What happens to a meal already in the destination and to any
// leftovers the move strands is the caller's choice (plan.MoveMeal); the
// board asks first (handleMealMoveImpact) whenever there is one to make.
//
//	POST /meals/{id}/move  {date, slot, occupied, leftovers}
func (s *Server) handleMealMove(w http.ResponseWriter, r *http.Request) {
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
	back := planURL(p)
	date, slot := r.FormValue("date"), r.FormValue("slot")
	occupied, leftovers := r.FormValue("occupied"), r.FormValue("leftovers")
	if !moveTarget(p, date, slot) || !plan.ValidMoveChoice(occupied, leftovers) {
		s.setNotify(w, NotifyDanger, "That isn't a slot on this plan.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	res, err := plan.MoveMeal(ctx, s.store, plan.MoveParams{
		MealID: id, Date: date, Slot: slot, Occupied: occupied, Leftovers: leftovers,
	})
	if errors.Is(err, plan.ErrMealLocked) {
		s.setNotify(w, NotifyDanger, "The meal in that slot is locked. Unlock it first, or swap the two instead.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		log.Printf("meal move %d -> %s %s: %v", id, date, slot, err)
		s.setNotify(w, NotifyDanger, "Couldn't move that meal.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	// Each day feeds its own number of people, so a meal that changed days is
	// brought to its new day's portions (and a swapped-back one to the old).
	if res.FromDay != date {
		s.rescaleDay(ctx, p.ID, date)
		s.rescaleDay(ctx, p.ID, res.FromDay)
	}

	// Quantity-only: a move never adds meal content, it only changes how much
	// of it is needed or drops a replaced meal's lines.
	s.repriceInBackground(p.ID, hh, true)
	s.setNotify(w, NotifySuccess, mealMoveMessage(res, date, slot, leftovers))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func mealMoveMessage(res *plan.MoveResult, date, slot, leftovers string) string {
	msg := fmt.Sprintf("%s moved to %s %s.", res.Title, dayLabel(date), slot)
	switch {
	case res.Replaced:
		msg += fmt.Sprintf(" %s was removed.", res.Displaced)
	case res.Displaced != "":
		msg += fmt.Sprintf(" %s took its place on %s %s.", res.Displaced, dayLabel(res.FromDay), res.FromSlot)
	}
	if res.Stranded > 0 {
		verb := "left without a source meal"
		if leftovers == plan.MoveClear {
			verb = "removed"
		}
		msg += fmt.Sprintf(" %d leftover %s %s.", res.Stranded, pluralize(res.Stranded, "meal", "meals"), verb)
	}
	return msg + " The shopping list is updating."
}

// handleMealDelete takes a meal off the plan, leaving its slot empty. Meals
// eating its leftovers go with it: left behind they would be leftovers of
// nothing, and the board says so before this is called.
//
//	POST /meals/{id}/delete
func (s *Server) handleMealDelete(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()

	// Read before deleting: DeleteMeal unhooks every leftover pointing at it.
	deps, _ := s.store.ListLeftoversSourcedFromMeal(ctx, id)
	if err := s.store.DeleteMeal(ctx, id); err != nil {
		log.Printf("meal delete %d: %v", id, err)
		s.setNotify(w, NotifyDanger, "Couldn't remove that meal.")
		http.Redirect(w, r, planURL(p), http.StatusSeeOther)
		return
	}
	removed := 0
	for _, d := range deps {
		if err := s.store.DeleteMeal(ctx, d.ID); err != nil {
			log.Printf("meal delete %d: leftover meal %d: %v", id, d.ID, err)
			continue
		}
		removed++
	}

	s.repriceInBackground(p.ID, hh, true)
	msg := fmt.Sprintf("%s removed from %s %s.", meal.Title, dayLabel(meal.Day), meal.Slot)
	if removed > 0 {
		msg += fmt.Sprintf(" %d leftover %s that ate it went too.", removed, pluralize(removed, "meal", "meals"))
	}
	s.setNotify(w, NotifySuccess, msg+" The shopping list is updating.")
	http.Redirect(w, r, planURL(p), http.StatusSeeOther)
}
