package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/csrf"

	"goeat/db"
	"goeat/middleware"
	"goeat/plan"
)

// handleHealth returns a JSON health check. Not behind auth; used by reverse
// proxies and uptime monitors.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK

	if err := s.store.Ping(r.Context()); err != nil {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  status,
		"version": s.version,
	})
}

type dashPageData struct {
	HasPlan         bool
	PlanStatus      string // "generating" | "ready" | "error" | ""
	WeekLabel       string // "Sep 1 - Sep 7"
	TotalLabel      string // "$47.20" or ""
	BudgetLabel     string // "$120"
	OverBudget      bool
	StatsSpent      string // "$47.20 / $120" or ""
	StatsOverBudget bool
	StatsMeals      int64
	StatsPlans      int
	RecentPlans     []*db.Plan // up to 8 for the history strip
	HasLLM          bool
}

// handleDashboard serves the main dashboard. RequireAuth middleware guarantees
// a user is in context; we additionally guard against the household not yet
// existing (setup not completed).
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	weekStart, weekEnd := plan.WeekBounds(now, s.cfg.WeekStartDay)
	from := weekStart.Format("2006-01-02")
	to := weekEnd.Format("2006-01-02")

	data := dashPageData{HasLLM: s.gen != nil}

	if p, _ := s.store.GetLatestPlan(ctx, hh.ID); p != nil {
		data.HasPlan = true
		data.PlanStatus = p.Status
		data.WeekLabel = fmt.Sprintf("%s - %s",
			fmtMonthDay(p.WeekStart), fmtMonthDay(p.WeekEnd))
		data.BudgetLabel = fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100)
		if p.TotalCents > 0 {
			data.TotalLabel = fmt.Sprintf("$%.2f", float64(p.TotalCents)/100)
			data.OverBudget = p.TotalCents > p.BudgetCents
		}
	}

	if st, _ := s.store.GetSpendStats(ctx, hh.ID, from, to); st != nil {
		if st.TotalCents > 0 || st.BudgetCents > 0 {
			data.StatsSpent = fmt.Sprintf("$%.2f / $%.0f",
				float64(st.TotalCents)/100, float64(st.BudgetCents)/100)
			data.StatsOverBudget = st.TotalCents > st.BudgetCents
		}
		data.StatsMeals = st.MealCount
		data.StatsPlans = st.PlanCount
	}

	if all, _ := s.store.ListPlans(ctx, hh.ID); len(all) > 0 {
		if len(all) > 8 {
			all = all[:8]
		}
		data.RecentPlans = all
	}

	s.render(w, r, "index", data)
}

func fmtMonthDay(isoDate string) string {
	t, err := time.Parse("2006-01-02", isoDate)
	if err != nil {
		return isoDate
	}
	return t.Format("Jan 2")
}

// handleCSRFError is the gorilla/csrf error handler - returns a plain 403 with
// the failure reason so clients get an actionable message.
func (s *Server) handleCSRFError(w http.ResponseWriter, r *http.Request) {
	reason := csrf.FailureReason(r)
	http.Error(w, "CSRF check failed: "+reason.Error(), http.StatusForbidden)
}
