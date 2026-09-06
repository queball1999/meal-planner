package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"goeat/db"
	"goeat/middleware"
	"goeat/plan"
	"goeat/pricing"
)

// calendarSlot holds one cell in the 7-day × 3-slot calendar grid.
type calendarSlot struct {
	MealID     int64
	Title      string
	Effort     string
	Servings   int
	Locked     bool
	IsLeftover bool
	IsEmpty    bool
}

// calendarDay holds one column in the calendar grid.
type calendarDay struct {
	Date      string // YYYY-MM-DD
	DateLabel string // "Mon Jan 2"
	Headcount int
	Slots     map[string]calendarSlot // "breakfast"|"lunch"|"dinner"
}

type planPageData struct {
	HasPlan           bool
	PlanID            int64
	WeekStart         string
	WeekEnd           string
	BudgetLabel       string
	TotalLabel        string
	OverBudget        bool
	ConfidenceSummary string
	Days              []calendarDay
	HasLLM            bool
	ReadOnly          bool // true when viewing a past plan via ?week=
}

var slotOrder = []string{"breakfast", "lunch", "dinner"}

// buildPricer returns a plan.Pricer that runs CostPlan using all configured stores.
func buildPricer(store db.Store, chain *pricing.Chain) plan.Pricer {
	if chain == nil {
		return nil
	}
	return func(ctx context.Context, planID int64, hh *db.Household) error {
		stores, err := store.ListStores(ctx, hh.ID)
		if err != nil {
			return err
		}
		_, err = pricing.CostPlan(ctx, store, chain, planID, hh, stores)
		return err
	}
}

func (s *Server) handlePlanPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	// ?week=YYYY-MM-DD loads a specific past plan in read-only mode.
	readOnly := false
	var p *db.Plan
	if week := r.URL.Query().Get("week"); week != "" {
		p, _ = s.store.GetPlanByWeekStart(ctx, hh.ID, week)
		readOnly = true
	} else {
		p, _ = s.store.GetLatestPlan(ctx, hh.ID)
	}

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
			MealID:     m.ID,
			Title:      m.Title,
			Effort:     m.Effort,
			Servings:   m.Servings,
			Locked:     m.Locked,
			IsLeftover: m.IsLeftover,
		}
	}

	// Load per-day headcount overrides.
	planDays, _ := s.store.ListPlanDays(ctx, p.ID)
	headcountByDate := make(map[string]int, len(planDays))
	for _, pd := range planDays {
		headcountByDate[pd.Date] = pd.Headcount
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
		hc := hh.HouseholdSize
		if override, ok := headcountByDate[dateStr]; ok && override > 0 {
			hc = override
		}
		days[i] = calendarDay{
			Date:      dateStr,
			DateLabel: date.Format("Mon Jan 2"),
			Headcount: hc,
			Slots:     slots,
		}
	}

	totalLabel := ""
	if p.TotalCents > 0 {
		totalLabel = fmt.Sprintf("$%.2f", float64(p.TotalCents)/100)
	}

	s.render(w, r, "plan", planPageData{
		HasPlan:           true,
		PlanID:            p.ID,
		WeekStart:         p.WeekStart,
		WeekEnd:           p.WeekEnd,
		BudgetLabel:       fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100),
		TotalLabel:        totalLabel,
		OverBudget:        p.TotalCents > p.BudgetCents && p.TotalCents > 0,
		ConfidenceSummary: p.ConfidenceSummary,
		Days:              days,
		HasLLM:            s.gen != nil,
		ReadOnly:          readOnly,
	})
}

// handlePlanHistory lists all past plans with optional date-range filtering.
// Accepts ?from=YYYY-MM-DD&to=YYYY-MM-DD; params are URL-reflected.
func (s *Server) handlePlanHistory(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	var plans []*db.Plan
	if from != "" && to != "" {
		plans, _ = s.store.ListPlansInRange(ctx, hh.ID, from, to)
	} else {
		plans, _ = s.store.ListPlans(ctx, hh.ID)
	}

	type historyPageData struct {
		Plans []*db.Plan
		From  string
		To    string
	}
	s.render(w, r, "history", historyPageData{Plans: plans, From: from, To: to})
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
	chain := s.chain
	hhID := hh.ID

	_, started := s.jobs.Start(r.Context(), hhID, func(j *plan.Job) {
		j.Emit(plan.JobEvent{Type: "status", Message: "Resolving preferences…"})

		ctx := context.Background()

		pricer := buildPricer(store, chain)
		planID, err := plan.Generate(ctx, store, gen, hhID, pricer)
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

// handlePlanHeadcount saves a per-day headcount override (§5.6).
func (s *Server) handlePlanHeadcount(w http.ResponseWriter, r *http.Request) {
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
	hcStr := r.FormValue("headcount")
	headcount, err := strconv.Atoi(hcStr)
	if err != nil || headcount < 1 {
		s.setFlash(w, "Headcount must be at least 1.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}
	_ = s.store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID:    p.ID,
		Date:      date,
		Headcount: headcount,
	})
	http.Redirect(w, r, "/plan", http.StatusSeeOther)
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
