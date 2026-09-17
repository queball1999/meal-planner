package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"goeat/catalog"
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
	Cost       string
	Locked     bool
	IsLeftover bool
	IsEmpty    bool
	// Status is "cooking" | "eating_out" | "skipped" (db.Meal.Status) - the
	// meal-card icons. Meaningless when IsEmpty.
	Status string
}

// calendarDay holds one column in the calendar grid.
type calendarDay struct {
	Date       string // YYYY-MM-DD
	DateLabel  string // "Mon Jan 2"
	Headcount  int
	Guests     int             // non-household people eating that day, part of Headcount
	GuestSlots map[string]bool // slots the guests eat; empty means every slot

	Slots map[string]calendarSlot // "breakfast"|"lunch"|"dinner"

	// Status is "cooking" | "eating_out" | "skipped"; StatusLabel is how it
	// reads in the day header. A non-cooking day contributes nothing to the
	// shopping list.
	Status      string
	StatusLabel string
	// EatingIDs is which household members are down to eat that day, used to
	// pre-tick the people picker.
	EatingIDs map[int64]bool
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
	Members           []*db.HouseholdMember // household people picker; empty falls back to a headcount box
	HasLLM            bool
	ReadOnly          bool   // true when viewing a past or canceled plan
	Canceled          bool   // true when viewing a plan superseded by a regenerate
	Status            string // "ready" | "generating" | "error" - effective, reconciled

	Tab  string                // "plan" (calendar) | "list" (shopping list)
	List *shoppingListPageData // populated when Tab == "list"

	// Sub-tab links, carrying over the current ?week=/?plan_id= selector so
	// switching tabs keeps the same week or historic plan in view.
	PlanTabURL string
	ListTabURL string

	// Week-nav toolbar (mirrors the dashboard calendar widget). Prev/Next step
	// the viewed week by 7 days; Today jumps back to the live week. The links
	// carry ?week=<start> so resolvePlanForRequest picks that week's plan.
	WeekNavPrevURL  string
	WeekNavNextURL  string
	WeekNavTodayURL string
	WeekNavTitle    string // "Sep 6 – Sep 12"

	// TodayMidWeek is true when today isn't the live week's first day. The
	// must-include modal's regenerate button (shown only when !ReadOnly, i.e.
	// always the live week here) uses it to offer "whole week or just the
	// remaining days" - see dashPageData.TodayMidWeek for the same flag on
	// the dashboard.
	TodayMidWeek bool
}

// weekNav holds the plan page's week-switcher links, computed once so the
// empty-state and full render paths stay in sync.
type weekNav struct {
	prev, next, today, title string
}

// buildWeekNav derives the toolbar links for the week starting at weekStart,
// anchored to basePath ("/plan" or "/plan/list") so stepping weeks keeps you
// on whichever sub-tab you were viewing instead of always bouncing to the
// calendar. The links carry ?week=<start> so resolvePlanForRequest resolves
// that week's plan; a week with no plan renders the empty state, which is the
// right answer.
//
// today must carry its own explicit ?week=<todayWeekStart> rather than just
// linking to the bare basePath: with no ?week= param at all, the page falls
// back to whichever plan GetLatestPlan resolves to (the most recently
// *generated* plan, not necessarily the current week's), so a bare link back
// to basePath could silently re-land on the same stale week instead of
// actually navigating to today.
func buildWeekNav(weekStart, todayWeekStart time.Time, basePath string) weekNav {
	weekEnd := weekStart.AddDate(0, 0, 6)
	return weekNav{
		prev:  basePath + "?week=" + weekStart.AddDate(0, 0, -7).Format("2006-01-02"),
		next:  basePath + "?week=" + weekStart.AddDate(0, 0, 7).Format("2006-01-02"),
		today: basePath + "?week=" + todayWeekStart.Format("2006-01-02"),
		title: weekStart.Format("Jan 2") + " – " + weekEnd.Format("Jan 2"),
	}
}

// planTabURL points a sub-tab link (Plan / Shopping list) at path while
// carrying over whichever selector (?plan_id= or ?week=) the current request
// used, so switching tabs keeps the same week or historic plan in view
// instead of silently dropping back to the latest one.
func planTabURL(r *http.Request, path string) string {
	if id := r.URL.Query().Get("plan_id"); id != "" {
		return path + "?plan_id=" + url.QueryEscape(id)
	}
	if week := r.URL.Query().Get("week"); week != "" {
		return path + "?week=" + url.QueryEscape(week)
	}
	return path
}

// planBasePath is the sub-tab's own path, used to anchor week-nav links so
// they stay on the tab the user is viewing.
func planBasePath(tab string) string {
	if tab == "list" {
		return "/plan/list"
	}
	return "/plan"
}

// planTab returns the requested sub-tab, defaulting to the calendar.
func planTab(r *http.Request) string {
	if r.URL.Path == "/plan/list" || r.URL.Query().Get("tab") == "list" {
		return "list"
	}
	return "plan"
}

// planNavSlug maps a plan sub-tab to the header nav key so exactly one nav
// item highlights ("Plan" for the calendar, "Shopping List" for the list).
func planNavSlug(tab string) string {
	if tab == "list" {
		return "list"
	}
	return "plan"
}

var slotOrder = []string{"breakfast", "lunch", "dinner"}

// buildPricer returns a plan.Pricer that runs CostPlan using all configured stores.
func buildPricer(store db.Store, chain *pricing.Chain) plan.Pricer {
	if chain == nil {
		return nil
	}
	return func(ctx context.Context, planID int64, hh *db.Household) error {
		// Link every ingredient to a catalog item first so CostPlan can
		// aggregate by item and use per-store package definitions.
		if err := catalog.LinkPlanIngredients(ctx, store, hh.ID, planID); err != nil {
			return err
		}
		stores, err := store.ListStores(ctx, hh.ID)
		if err != nil {
			return err
		}
		_, err = pricing.CostPlan(ctx, store, chain, planID, hh, stores)
		return err
	}
}

// buildRescalePricer is buildPricer for a quantity-only change - guests added
// to a day, or a meal skipped/marked eating-out - where what changed is how
// much of each ingredient is needed, not what the plan contains. It rescales
// already-priced lines in place instead of CostPlan's delete-and-reprice-
// everything, so an adjustment like this never re-asks a provider for a line
// that was already priced (see pricing.RescaleShoppingList).
func buildRescalePricer(store db.Store, chain *pricing.Chain) plan.Pricer {
	if chain == nil {
		return nil
	}
	return func(ctx context.Context, planID int64, hh *db.Household) error {
		if err := catalog.LinkPlanIngredients(ctx, store, hh.ID, planID); err != nil {
			return err
		}
		stores, err := store.ListStores(ctx, hh.ID)
		if err != nil {
			return err
		}
		_, err = pricing.RescaleShoppingList(ctx, store, chain, planID, hh, stores)
		return err
	}
}

// buildPriceChecker returns a plan.PriceChecker the generation tool loop can
// call mid-draft to ground an ingredient in a real price instead of an
// LLM guess - the fix for a plan whose every meal looked individually
// reasonable but whose total quietly blew past the household's budget.
// Tries every configured store, same order buildPricer's chain.Resolve loop
// uses, and returns the first hit.
func buildPriceChecker(store db.Store, chain *pricing.Chain) plan.PriceChecker {
	if chain == nil {
		return nil
	}
	return func(ctx context.Context, term string) (int64, string, string, bool) {
		hh, err := store.GetHousehold(ctx)
		if err != nil || hh == nil {
			return 0, "", "", false
		}
		stores, err := store.ListStores(ctx, hh.ID)
		if err != nil {
			return 0, "", "", false
		}
		for _, gs := range stores {
			r, rerr := chain.Resolve(ctx, term, gs.ID, hh.ZIPCode)
			if rerr != nil || r == nil {
				continue
			}
			return r.PriceCents, r.PurchaseUnit, gs.Name, true
		}
		return 0, "", "", false
	}
}

// reconcilePlanStatus is the single source of truth for a plan's state across
// the dashboard, the plan page, and the generation progress screen. A row left
// at "generating" by a job that is no longer running (timeout, panic, process
// restart) is repaired here: it becomes "ready" if meals were persisted, "error"
// if not. Returns the effective status and mutates p.Status to match.
func (s *Server) reconcilePlanStatus(ctx context.Context, hhID int64, p *db.Plan) string {
	if p == nil {
		return ""
	}
	if p.Status != "generating" {
		return p.Status
	}
	if s.jobs != nil && s.jobs.Get(hhID) != nil {
		return "generating" // a job really is working on it
	}
	effective := "error"
	if meals, _ := s.store.ListMealsByPlan(ctx, p.ID); len(meals) > 0 {
		effective = "ready"
	}
	_ = s.store.UpdatePlanStatus(ctx, p.ID, effective)
	p.Status = effective
	if effective == "ready" {
		// The normal success path (plan.Generate) retires other plans for the
		// same week once it confirms "ready" - do the same here, since a job
		// that died without updating status (e.g. a server restart mid-run)
		// bypasses that path entirely and leaves stale "ready" plans stacked
		// up for the same week.
		if err := s.store.CancelOtherPlansForWeek(ctx, hhID, p.WeekStart, p.ID); err != nil {
			log.Printf("reconcile: cancel other plans for week %s failed: %v", p.WeekStart, err)
		}
	}
	return effective
}

// resolvePlanForRequest picks which plan a /plan request (the calendar tab,
// the shopping list tab, or the list's polling fragment) is about, from
// ?plan_id=/?week=/neither, so every one of them agrees on the same week.
//
// ?plan_id=123 loads one specific plan (e.g. a canceled one from
// /plan/history - its own week now resolves to whatever superseded it, so it
// can only be reached by id) and is always read-only. ?week=YYYY-MM-DD loads
// that week's plan and is read-only unless it names the live week - the
// Prev/Next toolbar links always carry ?week=, so landing back on the current
// week that way (rather than via the bare "Today" link) must still be
// editable, not silently fall back to a past-plan view.
func (s *Server) resolvePlanForRequest(ctx context.Context, hh *db.Household, r *http.Request) (p *db.Plan, readOnly bool) {
	if idStr := r.URL.Query().Get("plan_id"); idStr != "" {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			if found, _ := s.store.GetPlanByID(ctx, id); found != nil && found.HouseholdID == hh.ID {
				p = found
			}
		}
		return p, true
	}
	liveWeekStart, _ := plan.WeekBounds(time.Now().UTC(), s.cfg.WeekStartDay)
	if week := r.URL.Query().Get("week"); week != "" {
		p, _ = s.store.GetPlanByWeekStart(ctx, hh.ID, week)
		return p, week != liveWeekStart.Format("2006-01-02")
	}
	p, _ = s.store.GetLatestPlan(ctx, hh.ID)
	// GetLatestPlan is "most recently created", not "current or later" - a
	// stale plan from a past week (nothing generated yet for this week, or
	// the newest plan got deleted) must not be shown as if it's the live
	// week's plan. Fall back to whatever plan actually belongs to the live
	// week, which may be none.
	if p != nil {
		if ws, err := time.Parse("2006-01-02", p.WeekStart); err == nil && ws.UTC().Before(liveWeekStart) {
			p, _ = s.store.GetPlanByWeekStart(ctx, hh.ID, liveWeekStart.Format("2006-01-02"))
		}
	}
	return p, false
}

// handlePlanListFragment serves just the shopping-list markup (no layout),
// so the "Shopping list" tab can poll it while pricing runs in the
// background after generation and swap in newly priced rows without a full
// page reload. See shopping_list_body.html's poll loop.
//
//	GET /plan/list/fragment
func (s *Server) handlePlanListFragment(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "no household", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	p, readOnly := s.resolvePlanForRequest(ctx, hh, r)
	listView := s.buildShoppingListView(ctx, hh, p, readOnly)
	s.renderFragment(w, r, "shoppingListBody", "list", planPageData{
		HasPlan:  listView.HasPlan,
		Tab:      "list",
		List:     &listView,
		ReadOnly: readOnly,
	})
}

// handlePlanListStopPricing aborts whichever background pricing run
// (priceInBackground after generation, or repriceInBackground after a
// day/meal edit) is currently filling in this plan's shopping list, leaving
// every not-yet-priced line on its own tier-3/default fallback instead of
// waiting for more live or AI lookups. See the "Stop pricing" button in
// shopping_list_body.html's pending-pricing banner.
//
//	POST /plan/list/stop-pricing
func (s *Server) handlePlanListStopPricing(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "no household", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	p, _ := s.resolvePlanForRequest(ctx, hh, r)
	if p == nil {
		http.Error(w, "no plan", http.StatusNotFound)
		return
	}
	plan.StopPricing(p.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlanPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tab := planTab(r)

	// Resolved once, up front, so the calendar tab and the shopping-list tab
	// always agree on which week they're showing - switching tabs used to
	// silently drop back to the latest week's shopping list even while
	// viewing a past plan.
	p, readOnly := s.resolvePlanForRequest(ctx, hh, r)

	var listView *shoppingListPageData
	if tab == "list" {
		v := s.buildShoppingListView(ctx, hh, p, readOnly)
		listView = &v
	}

	status := s.reconcilePlanStatus(ctx, hh.ID, p)

	// Week the toolbar is anchored to: the ?week= param when navigating, else
	// the resolved plan's week, else the live week. Computed once so the
	// empty-state and full render paths stay in sync.
	todayWeekStart, _ := plan.WeekBounds(time.Now().UTC(), s.cfg.WeekStartDay)
	todayMidWeek := !time.Now().UTC().Truncate(24 * time.Hour).Equal(todayWeekStart)
	viewedWeekStart := todayWeekStart
	if week := r.URL.Query().Get("week"); week != "" {
		if t, err := time.Parse("2006-01-02", week); err == nil {
			viewedWeekStart = t
		}
	} else if p != nil {
		if t, err := time.Parse("2006-01-02", p.WeekStart); err == nil {
			viewedWeekStart = t
		}
	}
	weekNav := buildWeekNav(viewedWeekStart, todayWeekStart, planBasePath(tab))
	planTabHref := planTabURL(r, "/plan")
	listTabHref := planTabURL(r, "/plan/list")

	// Show any plan that actually has meals, whatever its status - a plan
	// still finishing (or one that erred after persisting some meals) is far
	// more useful on screen than an empty "no plan" card. Only fall back to
	// the empty state when there is genuinely nothing to show.
	var meals []*db.Meal
	if p != nil {
		meals, _ = s.store.ListMealsByPlan(ctx, p.ID)
	}
	if p == nil || len(meals) == 0 {
		if status == "error" {
			s.setNotify(w, NotifyDanger, "The last plan generation failed. Check Settings → AI Logs for details, then regenerate.")
		} else if status == "generating" {
			s.setNotify(w, NotifyInfo, "Your plan is still generating. This page will fill in once it's ready.")
		}
		s.renderWithPage(w, r, "plan", planNavSlug(tab), planPageData{
			HasPlan:         false,
			HasLLM:          s.llmGen() != nil,
			Tab:             tab,
			List:            listView,
			PlanTabURL:      planTabHref,
			ListTabURL:      listTabHref,
			WeekNavPrevURL:  weekNav.prev,
			WeekNavNextURL:  weekNav.next,
			WeekNavTodayURL: weekNav.today,
			WeekNavTitle:    weekNav.title,
			TodayMidWeek:    todayMidWeek,
		})
		return
	}

	// Priced once per plan and split across meals below - a query per meal
	// would mean up to 21 identical lookups for one calendar page.
	var shoppingLines []*db.ShoppingListItem
	if p != nil {
		shoppingLines, _ = s.store.ListShoppingListItems(ctx, p.ID)
	}

	// Build a map keyed by "date|slot" for fast calendar lookup.
	mealMap := make(map[string]calendarSlot, len(meals))
	for _, m := range meals {
		key := m.Day + "|" + m.Slot
		cost := ""
		if len(shoppingLines) > 0 {
			ings, _ := s.store.ListIngredientsByMeal(ctx, m.ID)
			if cents := mealCostCentsFromLines(shoppingLines, ings); cents > 0 {
				cost = fmt.Sprintf("$%.2f", float64(cents)/100)
			}
		}
		mealMap[key] = calendarSlot{
			MealID:     m.ID,
			Title:      m.Title,
			Effort:     m.Effort,
			Servings:   m.Servings,
			Cost:       cost,
			Locked:     m.Locked,
			IsLeftover: m.IsLeftover,
			Status:     m.Status,
		}
	}

	// Load per-day overrides: who is eating and whether the day is cooked.
	planDays, _ := s.store.ListPlanDays(ctx, p.ID)
	dayByDate := make(map[string]*db.PlanDay, len(planDays))
	for _, pd := range planDays {
		dayByDate[pd.Date] = pd
	}
	members, _ := s.store.ListHouseholdMembers(ctx, hh.ID)

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
		guests := 0
		guestSlots := map[string]bool{}
		status := db.DayCooking
		eating := map[int64]bool{}
		if pd, ok := dayByDate[dateStr]; ok {
			if pd.Headcount > 0 {
				hc = pd.Headcount
			}
			guests = pd.Guests
			for _, sl := range pd.GuestSlots {
				guestSlots[sl] = true
			}
			if pd.Status != "" {
				status = pd.Status
			}
			for _, id := range pd.MemberIDs {
				eating[id] = true
			}
		}
		// A day with members but no recorded selection means "everyone" -
		// which is what generation seeds, and what an unticked picker would
		// otherwise misrepresent as nobody.
		if len(eating) == 0 {
			for _, m := range members {
				eating[m.ID] = true
			}
		}
		days[i] = calendarDay{
			Date:        dateStr,
			DateLabel:   date.Format("Mon Jan 2"),
			Headcount:   hc,
			Guests:      guests,
			GuestSlots:  guestSlots,
			Slots:       slots,
			Status:      status,
			StatusLabel: dayStatusLabel[status],
			EatingIDs:   eating,
		}
	}

	totalLabel := ""
	if p.TotalCents > 0 {
		totalLabel = fmt.Sprintf("$%.2f", float64(p.TotalCents)/100)
	}

	s.renderWithPage(w, r, "plan", planNavSlug(tab), planPageData{
		HasPlan:           true,
		Members:           members,
		PlanID:            p.ID,
		WeekStart:         p.WeekStart,
		WeekEnd:           p.WeekEnd,
		BudgetLabel:       fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100),
		TotalLabel:        totalLabel,
		OverBudget:        p.TotalCents > p.BudgetCents && p.TotalCents > 0,
		ConfidenceSummary: p.ConfidenceSummary,
		Days:              days,
		HasLLM:            s.llmGen() != nil,
		ReadOnly:          readOnly,
		Canceled:          p.Canceled,
		Status:            status,
		Tab:               tab,
		List:              listView,
		PlanTabURL:        planTabHref,
		ListTabURL:        listTabHref,
		WeekNavPrevURL:    weekNav.prev,
		WeekNavNextURL:    weekNav.next,
		WeekNavTodayURL:   weekNav.today,
		WeekNavTitle:      weekNav.title,
		TodayMidWeek:      todayMidWeek,
	})
}

// historyPlanRow pairs a plan with whether /plan/history (or the dashboard's
// plan-history card) should offer a Regenerate action for it - see
// canRegeneratePlan - and, when it does, whether that retry is for the live
// week on a day other than its first (TodayMidWeek) - the must-include
// modal's "whole week or just the remaining days" choice only makes sense
// then, same as everywhere else it's offered (dashPageData/
// planPageData.TodayMidWeek).
type historyPlanRow struct {
	*db.Plan
	CanRegenerate bool
	TodayMidWeek  bool
}

// canRegeneratePlan reports whether a retry from /plan/history would
// actually be accepted by handlePlanGenerate: the plan's own week is current
// or future (past weeks are refused there - see its comment), and the plan
// is the active, failed attempt for that week rather than one already
// superseded by a later regenerate (canceled) or one that finished fine
// (ready).
func canRegeneratePlan(p *db.Plan, curStart time.Time) bool {
	if p.Status != "error" || p.Canceled {
		return false
	}
	ws, err := time.Parse("2006-01-02", p.WeekStart)
	return err == nil && !ws.Before(curStart)
}

// planIsMidWeekRetry reports whether a regeneratable plan's own week is the
// live week on a day other than its first - see historyPlanRow.TodayMidWeek.
func planIsMidWeekRetry(p *db.Plan, curStart time.Time, todayMidWeek bool) bool {
	return todayMidWeek && p.WeekStart == curStart.Format("2006-01-02")
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

	now := time.Now()
	curStart, _ := plan.WeekBounds(now, s.cfg.WeekStartDay)
	todayMidWeek := !now.Truncate(24 * time.Hour).Equal(curStart)
	rows := make([]historyPlanRow, len(plans))
	for i, p := range plans {
		canRegen := canRegeneratePlan(p, curStart)
		rows[i] = historyPlanRow{
			Plan:          p,
			CanRegenerate: canRegen,
			TodayMidWeek:  canRegen && planIsMidWeekRetry(p, curStart, todayMidWeek),
		}
	}

	type historyPageData struct {
		Plans  []historyPlanRow
		From   string
		To     string
		Page   Pagination
		HasLLM bool
	}
	rows, page := paginate(r, rows)
	s.render(w, r, "history", historyPageData{Plans: rows, From: from, To: to, Page: page, HasLLM: s.llmGen() != nil})
}

// handlePlanHistoryDetail serves the JSON the history page's row-click modal
// renders - week/status/budget summary plus every meal grouped by day, so
// the household can see what a past (or failed) plan actually contained
// without leaving /plan/history to open the full calendar.
//
//	GET /plan/history/{id}/detail
func (s *Server) handlePlanHistoryDetail(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	p, err := s.store.GetPlanByID(ctx, id)
	if err != nil || p == nil || p.HouseholdID != hh.ID {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "plan not found"})
		return
	}

	meals, _ := s.store.ListMealsByPlan(ctx, p.ID)
	byDay := make(map[string][]map[string]any)
	var dayOrder []string
	for _, m := range meals {
		if _, seen := byDay[m.Day]; !seen {
			dayOrder = append(dayOrder, m.Day)
		}
		byDay[m.Day] = append(byDay[m.Day], map[string]any{
			"slot":  m.Slot,
			"title": m.Title,
		})
	}
	sort.Strings(dayOrder)
	days := make([]map[string]any, 0, len(dayOrder))
	for _, d := range dayOrder {
		days = append(days, map[string]any{
			"date_label": dayLabel(d),
			"meals":      byDay[d],
		})
	}

	totalLabel, budgetLabel := "", ""
	if p.TotalCents > 0 {
		totalLabel = fmt.Sprintf("$%.2f", float64(p.TotalCents)/100)
	}
	if p.BudgetCents > 0 {
		budgetLabel = fmt.Sprintf("$%.0f", float64(p.BudgetCents)/100)
	}

	now := time.Now()
	curStart, _ := plan.WeekBounds(now, s.cfg.WeekStartDay)
	todayMidWeek := !now.Truncate(24 * time.Hour).Equal(curStart)
	canRegen := canRegeneratePlan(p, curStart)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"plan_id":        p.ID,
		"week_start":     p.WeekStart,
		"week_label":     fmtMonthDay(p.WeekStart) + " - " + fmtMonthDay(p.WeekEnd),
		"status":         p.Status,
		"canceled":       p.Canceled,
		"total_label":    totalLabel,
		"budget_label":   budgetLabel,
		"confidence":     p.ConfidenceSummary,
		"meal_count":     len(meals),
		"days":           days,
		"can_view":       p.Status == "ready",
		"can_regenerate": canRegen,
		"mid_week":       canRegen && planIsMidWeekRetry(p, curStart, todayMidWeek),
		"has_llm":        s.llmGen() != nil,
	})
}

// handlePlanDelete removes a plan (and its meals/recipes/ingredients via
// cascade). Scoped to the caller's household.
func (s *Server) handlePlanDelete(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.DeletePlan(r.Context(), hh.ID, id); err != nil {
		s.setNotify(w, NotifyDanger, "Could not delete that plan.")
	} else {
		s.setNotify(w, NotifySuccess, "Plan deleted.")
	}

	// Land on history regardless of which page the delete came from.
	http.Redirect(w, r, "/plan/history", http.StatusSeeOther)
}

// runPlanGenerationJob wraps one call to plan.Generate/plan.GenerateForWeek in
// the job bookkeeping (status emission, hard timeout, success/error events)
// both entry points below share, so neither can drift from the other on how
// progress is reported.
func (s *Server) runPlanGenerationJob(hhID int64, generate func(ctx context.Context, pricer plan.Pricer, checker plan.PriceChecker, j *plan.Job) (int64, error)) (job *plan.Job, started bool) {
	pricer := buildPricer(s.store, s.priceChain())
	checker := buildPriceChecker(s.store, s.priceChain())

	return s.jobs.Start(context.Background(), hhID, func(j *plan.Job) {
		j.EmitStatus("Resolving preferences…")

		// Hard ceiling: a stalled LLM or pricing call must not leave the job
		// (and the progress screen) spinning forever. Costing a full week can
		// mean a live scrape or AI price estimate per ingredient (30+ calls),
		// so this needs real headroom - 5 minutes was cutting pricing off
		// mid-pass, silently leaving items unpriced and the plan total at $0.
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()

		j.EmitStatus("Asking the AI to build your week… (this can take a while depending on your provider)")

		planID, err := generate(ctx, pricer, checker, j)
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
}

// startPlanGeneration launches a background generation job for the upcoming
// week (plan.Generate → next Sunday), unless one is already running
// (single-flight, see plan.JobManager). Used by the auto-plan scheduler
// (§ RunAutoPlanScheduler); the dashboard's "Plan my week" / "Regenerate"
// buttons go through startPlanGenerationForWeek with an explicit week.
func (s *Server) startPlanGeneration(hhID int64) (job *plan.Job, started bool) {
	store, gen := s.store, s.llmGen()
	return s.runPlanGenerationJob(hhID, func(ctx context.Context, pricer plan.Pricer, checker plan.PriceChecker, j *plan.Job) (int64, error) {
		return plan.Generate(ctx, store, gen, hhID, pricer, checker, j)
	})
}

// startPlanGenerationForWeek is startPlanGeneration for one specific week -
// the dashboard's manual generate/regenerate action after navigating the
// calendar widget to a future week. See handlePlanGenerate for why past weeks
// never reach this. requested is the household's "make sure to include this
// week" list gathered on the generate form; nil when they asked for nothing
// specific. fromDate is plan.GenerateForWeek's "start here, not at weekStart"
// - pass weekStart itself for the normal full-week case.
func (s *Server) startPlanGenerationForWeek(hhID int64, weekStart, fromDate time.Time, requested []string) (job *plan.Job, started bool) {
	store, gen := s.store, s.llmGen()
	return s.runPlanGenerationJob(hhID, func(ctx context.Context, pricer plan.Pricer, checker plan.PriceChecker, j *plan.Job) (int64, error) {
		return plan.GenerateForWeek(ctx, store, gen, hhID, weekStart, fromDate, pricer, checker, j, requested)
	})
}

// handlePlanGenerate starts a background generation job, then redirects to
// the progress screen.
//
// With no "week" form field it plans the week containing today (honouring
// WEEK_START_DAY). An optional "week" field (YYYY-MM-DD, any day within the
// target week) asks for that specific week instead - the dashboard's
// generate/regenerate action once the calendar widget has been navigated away
// from the current week. Only the current week and future weeks are allowed:
// plan.Generate's callers throughout the app (the dashboard, the shopping
// list, day-status, meal-fill, ...) all resolve "the current plan" as
// whichever plan row was created most recently, on the assumption that plans
// are only ever created for now or later. Regenerating a week that has
// already ended would make that stale plan "the latest" everywhere else in
// the app - wrong shopping list, wrong headcount target, wrong everything -
// so it is refused here rather than silently corrupting those.
func (s *Server) handlePlanGenerate(w http.ResponseWriter, r *http.Request) {
	if s.llmGen() == nil {
		http.Error(w, "No LLM configured", http.StatusServiceUnavailable)
		return
	}
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	requested := s.buildRequestedMeals(r, hh.ID)
	// "scope=remaining" is the generate form's "just the remaining days"
	// choice, offered only when regenerating the live week mid-week (see
	// must_include_modal.html) - skip the days that have already happened
	// instead of asking (and paying) the LLM for meals nobody will eat.
	// Harmless if sent for a future week: fromDate (today) then falls before
	// that week's start, and plan.GenerateForWeek/daysFrom clamps that back
	// to the normal full week.
	fromDate := time.Time{}
	if r.FormValue("scope") == "remaining" {
		fromDate = time.Now()
	}

	if raw := strings.TrimSpace(r.FormValue("week")); raw != "" {
		asked, err := time.Parse("2006-01-02", raw)
		if err != nil {
			s.setNotify(w, NotifyDanger, "That isn't a valid week.")
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		weekStart, _ := plan.WeekBounds(asked, s.cfg.WeekStartDay)
		curStart, _ := plan.WeekBounds(time.Now(), s.cfg.WeekStartDay)
		if weekStart.Before(curStart) {
			s.setNotify(w, NotifyDanger, "Past weeks can't be regenerated - they're kept in Plan History for reference.")
			http.Redirect(w, r, "/?cal=week&calref="+weekStart.Format("2006-01-02"), http.StatusSeeOther)
			return
		}
		if fromDate.IsZero() {
			fromDate = weekStart
		}
		s.startPlanGenerationForWeek(hh.ID, weekStart, fromDate, requested)
		http.Redirect(w, r, "/plan/generate", http.StatusSeeOther)
		return
	}

	// No "week" field: plan the week that contains today (honouring
	// WEEK_START_DAY), not the next one. Same week the dashboard and calendar
	// treat as current.
	weekStart, _ := plan.WeekBounds(time.Now(), s.cfg.WeekStartDay)
	if fromDate.IsZero() {
		fromDate = weekStart
	}
	s.startPlanGenerationForWeek(hh.ID, weekStart, fromDate, requested)
	http.Redirect(w, r, "/plan/generate", http.StatusSeeOther)
}

// buildRequestedMeals reads the generate form's "meals you want to eat this
// week" inputs - recipe_ids (checked from the picker) and must_include_text
// (one free-typed request per line) - and turns them into prompt-ready
// strings for plan.GenerateForWeek's requested param. A recipe is rendered
// with its full ingredient list so the LLM reproduces it rather than
// reinventing something similar; a bad/foreign recipe_id is silently
// skipped rather than failing the whole generation.
func (s *Server) buildRequestedMeals(r *http.Request, householdID int64) []string {
	ctx := r.Context()
	_ = r.ParseForm()

	var out []string
	for _, idStr := range r.Form["recipe_ids"] {
		id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		if err != nil {
			continue
		}
		rec, err := s.store.GetCatalogRecipe(ctx, id)
		if err != nil || rec == nil || rec.HouseholdID != householdID {
			continue
		}
		desc := rec.Title
		if ings, ierr := s.store.ListCatalogRecipeIngredients(ctx, rec.ID); ierr == nil && len(ings) > 0 {
			parts := make([]string, 0, len(ings))
			for _, ing := range ings {
				parts = append(parts, strings.TrimSpace(strings.Join([]string{ing.Quantity, ing.Unit, ing.Name}, " ")))
			}
			desc += " - ingredients: " + strings.Join(parts, ", ")
		}
		out = append(out, desc+" (an existing recipe the household picked - use it as specified rather than inventing a substitute)")
	}

	for _, line := range strings.Split(r.FormValue("must_include_text"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (s *Server) handlePlanGeneratePage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, "plan_generate", nil)
}

// dayPortions works out what a day should be scaled to from a submitted form.
//
// Two shapes are accepted. `member[]` ids are the real one: the portion total
// is the sum of those members' factors, so two adults and two toddlers is 3.0
// portions rather than 4. A bare `headcount` is the fallback for a household
// that has never set members up, and means that many standard portions - which
// is exactly what this endpoint did before members existed.
//
// Guests are people who are not household members - a dinner party, a friend
// staying over. Each one eats a standard portion, so they add 1.0 to the
// portion total and 1 to the headcount on top of whoever is ticked.
//
// dayPortionInput is the resolved people-picker submission for one day.
type dayPortionInput struct {
	Portions       float64  // MemberPortions + Guests: the whole-day total
	MemberPortions float64  // household members only, before guests
	Headcount      int      // people count shown to the user (members + guests)
	Guests         int      // non-household people
	GuestSlots     []string // slots the guests eat; nil means every slot
}

// Returns the resolved input, and a message to show the user when it was unusable.
func (s *Server) dayPortions(ctx context.Context, householdID int64, r *http.Request) (dayPortionInput, string) {
	guests, _ := strconv.Atoi(r.FormValue("guests"))
	if guests < 0 {
		guests = 0
	}
	if guests > 50 {
		guests = 50
	}

	var guestSlots []string
	if guests > 0 {
		for _, sl := range r.Form["guest_slot"] {
			sl = strings.ToLower(strings.TrimSpace(sl))
			if sl == "breakfast" || sl == "lunch" || sl == "dinner" {
				guestSlots = append(guestSlots, sl)
			}
		}
	}

	raw := r.Form["member"]
	if len(raw) > 0 {
		ids := make([]int64, 0, len(raw))
		for _, v := range raw {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				continue
			}
			ids = append(ids, id)
		}
		portions, n, err := s.store.SumPortionFactors(ctx, householdID, ids)
		if err != nil {
			log.Printf("day portions: sum factors: %v", err)
			return dayPortionInput{}, "Couldn't work out portions for those people. Try again."
		}
		if n == 0 && guests == 0 {
			return dayPortionInput{}, "Pick at least one person eating that day."
		}
		return dayPortionInput{
			Portions: portions + float64(guests), MemberPortions: portions,
			Headcount: n + guests, Guests: guests, GuestSlots: guestSlots,
		}, ""
	}

	// No members ticked. A day that is only guests eating is still valid.
	if guests > 0 {
		return dayPortionInput{
			Portions: float64(guests), MemberPortions: 0,
			Headcount: guests, Guests: guests, GuestSlots: guestSlots,
		}, ""
	}

	headcount, err := strconv.Atoi(r.FormValue("headcount"))
	if err != nil || headcount < 1 {
		return dayPortionInput{}, "Headcount must be at least 1."
	}
	return dayPortionInput{
		Portions: float64(headcount), MemberPortions: float64(headcount), Headcount: headcount,
	}, ""
}

// handlePlanHeadcount saves who is eating on one day of the plan (§5.6) and
// rescales that day's meals to match: servings, cooked portions, the recipe
// yield, and every ingredient quantity are recomputed from the as-generated
// baseline (db.ScaleMealsForDay). The shopping list is then rebuilt in the
// background, since the quantities it aggregates have just changed.
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	in, msg := s.dayPortions(ctx, hh.ID, r)
	if msg != "" {
		s.setNotify(w, NotifyDanger, msg)
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	memberIDs := make([]int64, 0, len(r.Form["member"]))
	for _, v := range r.Form["member"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			memberIDs = append(memberIDs, id)
		}
	}

	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}
	if err := s.store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID:     p.ID,
		Date:       date,
		Headcount:  in.Headcount,
		MemberIDs:  memberIDs,
		Portions:   in.Portions,
		Guests:     in.Guests,
		GuestSlots: in.GuestSlots,
	}); err != nil {
		log.Printf("headcount: save plan day %s: %v", date, err)
		s.setNotify(w, NotifyDanger, "Couldn't save that headcount. Try again.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	scaled, err := s.scaleDayForInput(ctx, p.ID, date, in)
	if err != nil {
		// The headcount itself is saved; only the rescale failed. Say so
		// rather than implying the portions moved.
		log.Printf("headcount: scale meals for %s: %v", date, err)
		s.setNotify(w, NotifyDanger, "Saved who's eating, but the portions couldn't be rescaled. Check the logs.")
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	if scaled.MealsScaled == 0 {
		s.setNotify(w, NotifySuccess, fmt.Sprintf("Headcount for %s set to %d.", date, in.Headcount))
		http.Redirect(w, r, "/plan", http.StatusSeeOther)
		return
	}

	// Quantity-only: a headcount change rescales existing meals, it never
	// adds new ones.
	s.repriceInBackground(p.ID, hh, true)
	s.setNotify(w, NotifySuccess, fmt.Sprintf(
		"%s now serves %d%s - rescaled %d meals. The shopping list is updating.",
		date, in.Headcount, guestNote(in.Guests, in.GuestSlots), scaled.MealsScaled))
	http.Redirect(w, r, "/plan", http.StatusSeeOther)
}

// scaleDayForInput rescales a day from a resolved people-picker submission:
// the whole day moves to the household's own portions, and any slot the guests
// were restricted to also carries the guests.
func (s *Server) scaleDayForInput(ctx context.Context, planID int64, date string, in dayPortionInput) (db.ScaleDayResult, error) {
	if in.Guests == 0 {
		return s.store.ScaleMealsForDay(ctx, planID, date, in.Portions)
	}

	slots := in.GuestSlots
	if len(slots) == 0 {
		slots = []string{"breakfast", "lunch", "dinner"}
	}
	base := in.MemberPortions
	if base < 1 {
		base = 1 // a guests-only day still needs a floor for its other slots
	}
	slotPortions := make(map[string]float64, len(slots))
	for _, sl := range slots {
		slotPortions[sl] = in.MemberPortions + float64(in.Guests)
	}
	return s.store.ScaleMealsForDayBySlot(ctx, planID, date, base, slotPortions)
}

// guestNote is the " (incl. 2 guests at dinner)" clause on the save toast.
func guestNote(guests int, slots []string) string {
	if guests == 0 {
		return ""
	}
	who := "1 guest"
	if guests != 1 {
		who = fmt.Sprintf("%d guests", guests)
	}
	if len(slots) == 0 {
		return fmt.Sprintf(" (incl. %s)", who)
	}
	return fmt.Sprintf(" (incl. %s at %s)", who, strings.Join(slots, " & "))
}

// repriceInBackground rebuilds a plan's shopping list off the request path.
// Costing can make a live lookup per ingredient and run for minutes, which is
// far too long to hold a form POST open, so the user gets an immediate redirect
// and the list catches up. At most one rebuild per plan runs at a time.
//
// quantityOnly selects buildRescalePricer over buildPricer: true for a change
// that only alters how much of each ingredient is needed (guests added,
// meal skipped) so already-priced lines are rescaled rather than re-resolved
// from scratch. Anything that can change *what* the plan contains (a
// regenerate, a meal swap) must pass false.
func (s *Server) repriceInBackground(planID int64, hh *db.Household, quantityOnly bool) {
	var pricer plan.Pricer
	if quantityOnly {
		pricer = buildRescalePricer(s.store, s.priceChain())
	} else {
		pricer = buildPricer(s.store, s.priceChain())
	}

	s.repriceMu.Lock()
	if s.repricingPlans[planID] {
		s.repriceMu.Unlock()
		return
	}
	s.repricingPlans[planID] = true
	s.repriceMu.Unlock()

	go func() {
		defer func() {
			s.repriceMu.Lock()
			delete(s.repricingPlans, planID)
			s.repriceMu.Unlock()
		}()

		// Detached from the request: the response is long gone by now.
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()

		// Lets the shopping list's "stop pricing" button abort this run early -
		// see plan.StopPricing.
		plan.RegisterPricingCancel(planID, cancel)
		defer plan.ClearPricingCancel(planID)

		if pricer != nil {
			if err := pricer(ctx, planID, hh); err != nil {
				log.Printf("reprice: costing plan %d: %v", planID, err)
			}
		}
		// Same guarantee as generation: the plan keeps a shopping list even
		// when pricing is unavailable or failed.
		if _, err := pricing.EnsureShoppingList(ctx, s.store, planID, hh); err != nil {
			log.Printf("reprice: shopping list fallback for plan %d: %v", planID, err)
		}
	}()
}

// handlePlanGenerateStatus streams SSE events for the active generation job.
func (s *Server) handlePlanGenerateStatus(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	// The server sets a 30s WriteTimeout for ordinary requests, but a
	// generation stream stays open for minutes. Without clearing the write
	// deadline the response is severed mid-run, the browser reports a
	// connection error, and the progress screen shows "Generation failed"
	// even though the background job goes on to finish the plan.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	job := s.jobs.Get(hh.ID)
	if job == nil {
		// No active job: it finished (or never ran) before the browser
		// connected. Report the real outcome from the plan row so the
		// progress screen doesn't bounce the user to an empty plan page.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		event, msg := "done", "no active job"
		if p, _ := s.store.GetLatestPlan(r.Context(), hh.ID); p != nil {
			if s.reconcilePlanStatus(r.Context(), hh.ID, p) == "error" {
				event = "error"
				msg = "The last plan generation didn't finish. Check Settings → AI Logs, then regenerate."
			}
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, msg)
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
