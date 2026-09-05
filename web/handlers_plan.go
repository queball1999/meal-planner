package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"goeat/middleware"
	"goeat/plan"
)

// calendarSlot holds one cell in the 7-day × 3-slot calendar grid.
type calendarSlot struct {
	MealID   int64
	Title    string
	Effort   string
	Servings int
	Locked   bool
	IsEmpty  bool
}

// calendarDay holds one column in the calendar grid.
type calendarDay struct {
	DateLabel string // "Mon Jan 2"
	Slots     map[string]calendarSlot // "breakfast"|"lunch"|"dinner"
}

type planPageData struct {
	HasPlan     bool
	PlanID      int64
	WeekStart   string
	WeekEnd     string
	BudgetLabel string
	Days        []calendarDay
	HasLLM      bool
}

var slotOrder = []string{"breakfast", "lunch", "dinner"}

func (s *Server) handlePlanPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil || p.Status == "generating" {
		s.render(w, r, "plan", planPageData{HasPlan: false, HasLLM: s.gen != nil})
		return
	}

	meals, _ := s.store.ListMealsByPlan(ctx, p.ID)

	// Build a map keyed by "date|slot" for fast calendar lookup.
	mealMap := make(map[string]calendarSlot, len(meals))
	for _, m := range meals {
		key := m.Day + "|" + m.Slot
		mealMap[key] = calendarSlot{
			MealID:   m.ID,
			Title:    m.Title,
			Effort:   m.Effort,
			Servings: m.Servings,
			Locked:   m.Locked,
		}
	}

	// Build the 7-day column slice ordered by date.
	weekStart, _ := time.Parse("2006-01-02", p.WeekStart)
	days := make([]calendarDay, 7)
	for i := range days {
		date := weekStart.AddDate(0, 0, i)
		dateStr := date.Format("2006-01-02")
		slots := make(map[string]calendarSlot, 3)
		for _, slot := range slotOrder {
			key := dateStr + "|" + slot
			if cs, ok := mealMap[key]; ok {
				slots[slot] = cs
			} else {
				slots[slot] = calendarSlot{IsEmpty: true}
			}
		}
		days[i] = calendarDay{
			DateLabel: date.Format("Mon Jan 2"),
			Slots:     slots,
		}
	}

	s.render(w, r, "plan", planPageData{
		HasPlan:     true,
		PlanID:      p.ID,
		WeekStart:   p.WeekStart,
		WeekEnd:     p.WeekEnd,
		BudgetLabel: fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100),
		Days:        days,
		HasLLM:      s.gen != nil,
	})
}

// handlePlanGenerate starts a background generation job, then redirects to
// the progress screen.
func (s *Server) handlePlanGenerate(w http.ResponseWriter, r *http.Request) {
	if s.gen == nil {
		http.Error(w, "No LLM configured", http.StatusServiceUnavailable)
		return
	}
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	store := s.store
	gen := s.gen
	hhID := hh.ID

	_, started := s.jobs.Start(r.Context(), hhID, func(j *plan.Job) {
		j.Emit(plan.JobEvent{Type: "status", Message: "Resolving preferences…"})

		ctx := context.Background()
		planID, err := plan.Generate(ctx, store, gen, hhID)
		if err != nil {
			log.Printf("plan generation error household=%d: %v", hhID, err)
			j.Status = plan.JobFailed
			j.Error = err.Error()
			j.Emit(plan.JobEvent{Type: "error", Message: err.Error()})
			return
		}
		j.Status = plan.JobDone
		j.PlanID = planID
		j.Emit(plan.JobEvent{Type: "done", Message: "Plan ready", PlanID: planID})
	})

	if !started {
		// Already running — just redirect to the progress page.
	}
	http.Redirect(w, r, "/plan/generate", http.StatusSeeOther)
}

func (s *Server) handlePlanGeneratePage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, "plan_generate", nil)
}

// handlePlanGenerateStatus streams SSE events for the active generation job.
func (s *Server) handlePlanGenerateStatus(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	job := s.jobs.Get(hh.ID)
	if job == nil {
		// No active job — send a synthetic done so the client can redirect.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fmt.Fprintf(w, "event: done\ndata: no active job\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	job.Subscribe(w)
}
