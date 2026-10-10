package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"goeat/db"
	"goeat/middleware"
)

// dayStatusLabel is how a status reads in a sentence.
var dayStatusLabel = map[string]string{
	db.DayCooking:   "cooking",
	db.DayEatingOut: "eating out",
	db.DaySkipped:   "skipped",
}

// orphanedMeal is one meal that loses its food when a day stops being cooked.
type orphanedMeal struct {
	MealID    int64  `json:"meal_id"`
	Title     string `json:"title"`
	Date      string `json:"date"`
	DateLabel string `json:"date_label"`
	Slot      string `json:"slot"`
}

// dayStatusImpact is what the resolution dialog is built from.
type dayStatusImpact struct {
	OK       bool           `json:"ok"`
	Date     string         `json:"date"`
	Orphaned []orphanedMeal `json:"orphaned"`
}

// handleDayStatusImpact answers "what breaks if I mark this day?" before
// anything is changed.
//
// The thing that breaks is leftovers. A batch-cooked Tuesday dinner can be
// feeding Wednesday, and marking Tuesday "eating out" silently empties
// Wednesday's plate. A plan that quietly loses a meal is worse than one that
// asks, so the UI asks first and this is what it asks.
func (s *Server) handleDayStatusImpact(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	date := r.PathValue("date")
	ctx := r.Context()

	p := s.planForDate(ctx, hh, date)
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no plan"})
		return
	}

	meals, err := s.store.ListLeftoversSourcedFrom(ctx, p.ID, date)
	if err != nil {
		log.Printf("day status impact %s: %v", date, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't check that"})
		return
	}

	out := dayStatusImpact{OK: true, Date: date, Orphaned: []orphanedMeal{}}
	for _, m := range meals {
		out.Orphaned = append(out.Orphaned, orphanedMeal{
			MealID:    m.ID,
			Title:     m.Title,
			Date:      m.Day,
			DateLabel: dayLabel(m.Day),
			Slot:      m.Slot,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDayStatus sets a day's status and applies the chosen resolution for
// any leftovers that were living off it.
//
// Resolutions (form field "resolution"):
//
//	cascade - mark the dependent days with the same status. "We're out
//	          Tuesday, so Wednesday's leftovers aren't happening either."
//	clear   - drop the orphaned meals, leaving those slots empty to fill.
//	replace - clear them, then reopen the plan with the recipe picker aimed at
//	          the first emptied slot. The picker cannot run inside this request
//	          (the meals have to be gone before a replacement is chosen), so
//	          the slot rides back on the redirect as ?fill=date|slot.
//	ignore  - leave them alone. Chosen deliberately when the cook plans to
//	          make something anyway; the meal keeps its leftover flag, which
//	          is wrong-ish, but it is the user's call and nothing is lost.
//
// Anything else (including an absent field) means "ignore", so a client that
// does not know about resolutions still gets a working status change.
func (s *Server) handleDayStatus(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	date := r.PathValue("date")
	if date == "" {
		http.Error(w, "missing date", http.StatusBadRequest)
		return
	}
	status := r.FormValue("status")
	if !db.ValidDayStatus(status) {
		s.setNotify(w, NotifyDanger, "That isn't a day status.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	p := s.planForDate(ctx, hh, date)
	if p == nil {
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	// Read the dependents before changing anything: once the status is set,
	// a cascade would look at a day that is already marked.
	var orphans []*db.Meal
	if status != db.DayCooking {
		orphans, _ = s.store.ListLeftoversSourcedFrom(ctx, p.ID, date)
	}

	if err := s.store.SetPlanDayStatus(ctx, p.ID, date, status); err != nil {
		log.Printf("day status %s -> %s: %v", date, status, err)
		s.setNotify(w, NotifyDanger, "Couldn't save that. Try again.")
		http.Redirect(w, r, planURL(p), http.StatusSeeOther)
		return
	}

	resolved := 0
	resolution := r.FormValue("resolution")
	// "replace" is "clear" plus a follow-up, so it shares the clearing branch.
	var fillDate, fillSlot string
	if resolution == "replace" && len(orphans) > 0 {
		fillDate, fillSlot = orphans[0].Day, orphans[0].Slot
	}

	switch resolution {
	case "cascade":
		// Dedupe: several orphaned meals can sit on the same day.
		seen := map[string]bool{date: true}
		for _, m := range orphans {
			if seen[m.Day] {
				continue
			}
			seen[m.Day] = true
			if err := s.store.SetPlanDayStatus(ctx, p.ID, m.Day, status); err != nil {
				log.Printf("day status cascade %s: %v", m.Day, err)
				continue
			}
			resolved++
		}
	case "clear", "replace":
		for _, m := range orphans {
			if err := s.store.DeleteMeal(ctx, m.ID); err != nil {
				log.Printf("day status clear meal %d: %v", m.ID, err)
				continue
			}
			resolved++
		}
	}

	// The shopping list is built from cooking days only, so any status change
	// moves it - as does dropping meals. Quantity-only: this never adds new
	// meal content, only drops or restores what was already planned.
	s.repriceInBackground(p.ID, hh, true)

	s.setNotify(w, NotifySuccess, dayStatusMessage(date, status, resolved, resolution))

	dest := planURL(p)
	if fillDate != "" {
		dest += "&fill=" + url.QueryEscape(fillDate+"|"+fillSlot)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func dayStatusMessage(date, status string, resolved int, resolution string) string {
	label := dayStatusLabel[status]
	base := fmt.Sprintf("%s marked %s. The shopping list is updating.", dayLabel(date), label)
	if resolved == 0 {
		return base
	}
	switch resolution {
	case "cascade":
		return fmt.Sprintf("%s marked %s, along with %d day(s) that were living off it. The shopping list is updating.",
			dayLabel(date), label, resolved)
	case "replace":
		return fmt.Sprintf("%s marked %s and %d leftover meal(s) cleared. Pick something for the first slot.",
			dayLabel(date), label, resolved)
	case "clear":
		return fmt.Sprintf("%s marked %s and %d leftover meal(s) cleared. The shopping list is updating.",
			dayLabel(date), label, resolved)
	}
	return base
}

// dayLabel renders a YYYY-MM-DD as "Mon Jan 2", falling back to the raw string
// for anything unparseable rather than showing a zero date.
func dayLabel(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("Mon Jan 2")
}
