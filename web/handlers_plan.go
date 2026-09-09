package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
// can only be reached by id). ?week=YYYY-MM-DD loads that week's current
// plan. Either way the view is read-only.
func (s *Server) resolvePlanForRequest(ctx context.Context, hh *db.Household, r *http.Request) (p *db.Plan, readOnly bool) {
	if idStr := r.URL.Query().Get("plan_id"); idStr != "" {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			if found, _ := s.store.GetPlanByID(ctx, id); found != nil && found.HouseholdID == hh.ID {
				p = found
			}
		}
		return p, true
	}
	if week := r.URL.Query().Get("week"); week != "" {
		p, _ = s.store.GetPlanByWeekStart(ctx, hh.ID, week)
		return p, true
	}
	p, _ = s.store.GetLatestPlan(ctx, hh.ID)
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
		s.renderWithPage(w, r, "plan", planNavSlug(tab), planPageData{HasPlan: false, HasLLM: s.llmGen() != nil, Tab: tab, List: listView})
		return
	}

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
		Page  Pagination
	}
	plans, page := paginate(r, plans)
	s.render(w, r, "history", historyPageData{Plans: plans, From: from, To: to, Page: page})
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
// never reach this.
func (s *Server) startPlanGenerationForWeek(hhID int64, weekStart time.Time) (job *plan.Job, started bool) {
	store, gen := s.store, s.llmGen()
	return s.runPlanGenerationJob(hhID, func(ctx context.Context, pricer plan.Pricer, checker plan.PriceChecker, j *plan.Job) (int64, error) {
		return plan.GenerateForWeek(ctx, store, gen, hhID, weekStart, pricer, checker, j)
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
		s.startPlanGenerationForWeek(hh.ID, weekStart)
		http.Redirect(w, r, "/plan/generate", http.StatusSeeOther)
		return
	}

	// No "week" field: plan the week that contains today (honouring
	// WEEK_START_DAY), not the next one. Same week the dashboard and calendar
	// treat as current.
	weekStart, _ := plan.WeekBounds(time.Now(), s.cfg.WeekStartDay)
	s.startPlanGenerationForWeek(hh.ID, weekStart)
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

	s.repriceInBackground(p.ID, hh)
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
func (s *Server) repriceInBackground(planID int64, hh *db.Household) {
	pricer := buildPricer(s.store, s.priceChain())

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
