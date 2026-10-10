package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	csrf "filippo.io/csrf/gorilla"

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
	HasPlan     bool
	PlanStatus  string // "generating" | "ready" | "error" | ""
	WeekLabel   string // "Sep 1 - Sep 7"
	TotalLabel  string // "$47.20" or ""
	BudgetLabel string // "$120"
	OverBudget  bool
	// StatsSpent is what has actually been bought so far: the sum of the
	// shopping-list lines checked off. This is the "This week spent" number -
	// real money out the door, not the plan's estimate.
	StatsSpent      string // "$47.20" or ""
	StatsBudget     string // "$120" or ""
	StatsOverBudget bool
	StatsMeals      int64
	StatsPlans      int
	RecentPlans     []historyPlanRow // up to 8 for the history strip
	HasLLM          bool
	Calendar        dashCalendar

	// The plan card and the spend stats track whichever week the calendar
	// widget is showing (?calref, week mode only). IsCurrentWeek is false when
	// the calendar has been navigated away from the live week; the card then
	// drops its generate/regenerate buttons - those only ever act on the
	// current week - and points "View plan" at that specific week instead.
	IsCurrentWeek bool
	IsPastWeek    bool   // true when the viewed week has already ended - never generate/regenerate
	WeekNavLabel  string // "Sep 8 – Sep 14", shown when IsCurrentWeek is false
	WeekParam     string // "2026-09-08" - the viewed week's start, for the generate form's hidden field
	PlanWeekParam string // "?week=2026-09-08" when viewing a past/future week, else ""
	PlanWeekStart string // the shown plan's own week start - what its Regenerate replans

	// PlanWeekAhead is set when the live-week dashboard is showing a plan for a
	// later week (generation plans the next full week once this one is partway
	// through). The stats strip and calendar then follow that plan's week so
	// its meals and budget render instead of a blank current week.
	PlanWeekAhead bool

	// TodayMidWeek is true when today isn't the live week's first day - only
	// meaningful together with IsCurrentWeek. The must-include modal's
	// "whole week or just the remaining days" choice only makes sense then;
	// asking it for a future week (nothing has happened yet) or a past one
	// (not generatable at all) would be a no-op either way.
	TodayMidWeek bool
}

// dashCalendar is the dashboard's schedule widget: a week (default) or month
// grid of scheduled meals.
type dashCalendar struct {
	Mode     string         // "week" | "month"
	Title    string         // "Sep 1 – Sep 7" or "September 2026"
	PrevURL  string         // link to the previous week/month
	NextURL  string         // link to the next week/month
	TodayURL string         // link back to the current week/month
	WeekURL  string         // toggle target for week mode
	MonthURL string         // toggle target for month mode
	Rows     [][]calDayCell // one inner slice per calendar row (7 cells)
	Empty    bool           // true when no meals fall in the visible range
}

type calDayCell struct {
	Date    string // YYYY-MM-DD
	DayNum  int    // day-of-month
	Weekday string // "Mon"
	InScope bool   // month view: day belongs to the displayed month
	IsToday bool
	Meals   []calMeal
}

type calMeal struct {
	MealID int64
	Slot   string // breakfast | lunch | dinner
	Title  string
}

// buildDashCalendar assembles the calendar widget from the ?cal / ?calref query
// params and the meals visible in the resulting date range. now fixes the
// "today" highlight; defaultRef is the week/month the grid opens on when there
// is no ?calref (usually today, but the plan's week when that runs ahead).
func buildDashCalendar(r *http.Request, store db.Store, householdID int64, weekStartDay string, now, defaultRef time.Time) dashCalendar {
	mode := r.URL.Query().Get("cal")
	if mode != "month" {
		mode = "week"
	}
	today := now.UTC().Truncate(24 * time.Hour)
	ref := defaultRef.UTC().Truncate(24 * time.Hour)
	if v := r.URL.Query().Get("calref"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			ref = t.UTC().Truncate(24 * time.Hour)
		}
	}

	cal := dashCalendar{Mode: mode}
	var gridStart time.Time
	var rowCount int

	if mode == "month" {
		monthStart := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, time.UTC)
		gs, _ := plan.WeekBounds(monthStart, weekStartDay)
		gridStart = gs
		rowCount = 6
		cal.Title = ref.Format("January 2006")
		cal.PrevURL = "/?cal=month&calref=" + monthStart.AddDate(0, -1, 0).Format("2006-01-02")
		cal.NextURL = "/?cal=month&calref=" + monthStart.AddDate(0, 1, 0).Format("2006-01-02")
	} else {
		ws, we := plan.WeekBounds(ref, weekStartDay)
		gridStart = ws
		rowCount = 1
		cal.Title = ws.Format("Jan 2") + " – " + we.Format("Jan 2")
		cal.PrevURL = "/?cal=week&calref=" + ws.AddDate(0, 0, -7).Format("2006-01-02")
		cal.NextURL = "/?cal=week&calref=" + ws.AddDate(0, 0, 7).Format("2006-01-02")
	}
	cal.TodayURL = "/?cal=" + mode
	cal.WeekURL = "/?cal=week&calref=" + ref.Format("2006-01-02")
	cal.MonthURL = "/?cal=month&calref=" + ref.Format("2006-01-02")

	gridEnd := gridStart.AddDate(0, 0, rowCount*7-1)
	meals, _ := store.ListMealsByHouseholdRange(r.Context(), householdID,
		gridStart.Format("2006-01-02"), gridEnd.Format("2006-01-02"))
	byDay := make(map[string][]calMeal, len(meals))
	for _, m := range meals {
		byDay[m.Day] = append(byDay[m.Day], calMeal{MealID: m.ID, Slot: m.Slot, Title: m.Title})
	}
	cal.Empty = len(meals) == 0

	cal.Rows = make([][]calDayCell, rowCount)
	for row := 0; row < rowCount; row++ {
		cells := make([]calDayCell, 7)
		for col := 0; col < 7; col++ {
			d := gridStart.AddDate(0, 0, row*7+col)
			ds := d.Format("2006-01-02")
			cells[col] = calDayCell{
				Date:    ds,
				DayNum:  d.Day(),
				Weekday: d.Format("Mon"),
				InScope: mode == "week" || d.Month() == ref.Month(),
				IsToday: ds == today.Format("2006-01-02"),
				Meals:   byDay[ds],
			}
		}
		cal.Rows[row] = cells
	}
	return cal
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
	curStart, _ := plan.WeekBounds(now, s.cfg.WeekStartDay)

	// The plan card and spend stats follow the calendar widget's week. Only in
	// week mode: a month view's ?calref is the 1st, and realigning to "the week
	// containing the 1st" would be an arbitrary slice of the month.
	ref := now
	if r.URL.Query().Get("cal") != "month" {
		if v := r.URL.Query().Get("calref"); v != "" {
			if t, err := time.Parse("2006-01-02", v); err == nil {
				ref = t.UTC()
			}
		}
	}
	weekStart, weekEnd := plan.WeekBounds(ref, s.cfg.WeekStartDay)
	from := weekStart.Format("2006-01-02")
	to := weekEnd.Format("2006-01-02")
	isCurrentWeek := weekStart.Equal(curStart)

	data := dashPageData{
		HasLLM:        s.llmGen() != nil,
		IsCurrentWeek: isCurrentWeek,
		IsPastWeek:    weekStart.Before(curStart),
		WeekParam:     from,
		TodayMidWeek:  !now.Truncate(24 * time.Hour).Equal(curStart),
	}
	if !isCurrentWeek {
		data.WeekNavLabel = weekStart.Format("Jan 2") + " – " + weekEnd.Format("Jan 2")
		data.PlanWeekParam = "?week=" + from
	}

	// Current week keeps the old behaviour (show the latest plan, which may be
	// an auto-generated one for next week); a navigated week resolves to that
	// specific week's plan.
	var p *db.Plan
	if isCurrentWeek {
		p, _ = s.store.GetLatestPlan(ctx, hh.ID)
		// GetLatestPlan is "most recently created", not "current or later" -
		// if nothing has been generated for the current week yet but an old
		// plan from a past week still exists (e.g. it was never regenerated,
		// or the newest plan got deleted), that stale plan is still the
		// "latest" one and must not be mislabeled "This week". Only follow
		// it forward (current/future); otherwise fall back to whatever plan
		// actually belongs to the current week, which may be none.
		if p != nil {
			if ws, err := time.Parse("2006-01-02", p.WeekStart); err == nil && ws.UTC().Before(curStart) {
				p, _ = s.store.GetPlanByWeekStart(ctx, hh.ID, from)
			}
		}
		// A failed run for a later week must not hide this week's plan.
		latestWeek := ""
		if p != nil {
			latestWeek = p.WeekStart
		}
		p = s.usableDashPlan(ctx, hh.ID, p)
		if p == nil && latestWeek != "" && latestWeek != from {
			cur, _ := s.store.GetPlanByWeekStart(ctx, hh.ID, from)
			p = s.usableDashPlan(ctx, hh.ID, cur)
		}
	} else {
		p, _ = s.store.GetPlanByWeekStart(ctx, hh.ID, from)
		p = s.usableDashPlan(ctx, hh.ID, p)
	}

	// The live-week view: if the latest plan is for a week other than the one
	// containing today (generation plans the next full week once this one is
	// underway), follow it - otherwise the stats strip and calendar, both
	// anchored to today's week, show nothing while the card shows the plan.
	calRef := now
	if isCurrentWeek && p != nil && p.WeekStart != from {
		if ws, err := time.Parse("2006-01-02", p.WeekStart); err == nil {
			weekStart, weekEnd = plan.WeekBounds(ws.UTC(), s.cfg.WeekStartDay)
			from = weekStart.Format("2006-01-02")
			to = weekEnd.Format("2006-01-02")
			calRef = ws.UTC()
			data.PlanWeekAhead = weekStart.After(curStart)
		}
	}

	if p != nil {
		data.HasPlan = true
		data.PlanWeekStart = p.WeekStart
		data.PlanStatus = p.Status // already reconciled by usableDashPlan
		data.WeekLabel = fmt.Sprintf("%s - %s",
			fmtMonthDay(p.WeekStart), fmtMonthDay(p.WeekEnd))
		data.BudgetLabel = fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100)
		if p.TotalCents > 0 {
			data.TotalLabel = fmt.Sprintf("$%.2f", float64(p.TotalCents)/100)
			data.OverBudget = p.TotalCents > p.BudgetCents
		}
	}

	if st, _ := s.store.GetSpendStats(ctx, hh.ID, from, to); st != nil {
		data.StatsMeals = st.MealCount
		data.StatsPlans = st.PlanCount
	}

	// "This week spent" is what has actually been bought: the sum of the
	// shopping-list lines checked off. The plan's estimated total and budget
	// give the "of $X budget" context and the over-budget flag.
	if p != nil {
		if spend, err := s.store.GetShoppingListSpend(ctx, p.ID); err == nil && spend != nil {
			data.StatsSpent = fmt.Sprintf("$%.2f", float64(spend.ActualCents)/100)
			if p.BudgetCents > 0 {
				data.StatsBudget = fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100)
			}
			data.StatsOverBudget = spend.ActualCents > p.BudgetCents && p.BudgetCents > 0
		}
	}

	if all, _ := s.store.ListPlans(ctx, hh.ID); len(all) > 0 {
		if len(all) > 8 {
			all = all[:8]
		}
		rows := make([]historyPlanRow, len(all))
		for i, p := range all {
			canRegen := canRegeneratePlan(p, curStart)
			rows[i] = historyPlanRow{
				Plan:          p,
				CanRegenerate: canRegen,
				TodayMidWeek:  canRegen && planIsMidWeekRetry(p, curStart, data.TodayMidWeek),
			}
		}
		data.RecentPlans = rows
	}

	data.Calendar = buildDashCalendar(r, s.store, hh.ID, s.cfg.WeekStartDay, now, calRef)

	s.render(w, r, "index", data)
}

// usableDashPlan reconciles p's status and returns the plan the dashboard card
// should show for p's week. A failed generation ("error") has no meals to view,
// so it is skipped in favour of the newest still-active plan for the same week
// that did succeed - a failed regenerate leaves the previous plan un-canceled.
// Returns nil when the week has no usable plan, so the card shows "No plan
// yet"; the failed run stays retryable from the recent-plans strip.
func (s *Server) usableDashPlan(ctx context.Context, hhID int64, p *db.Plan) *db.Plan {
	if p == nil || s.reconcilePlanStatus(ctx, hhID, p) != "error" {
		return p
	}
	all, _ := s.store.ListPlans(ctx, hhID)
	for _, q := range all {
		if q.ID == p.ID || q.Canceled || q.WeekStart != p.WeekStart || q.Status == "error" {
			continue
		}
		if s.reconcilePlanStatus(ctx, hhID, q) != "error" {
			return q
		}
	}
	return nil
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
