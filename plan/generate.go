package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"goeat/catalog"
	"goeat/db"
	"goeat/llm"
	"goeat/pricing"
)

var dayOffset = map[string]int{
	"sunday":    0,
	"monday":    1,
	"tuesday":   2,
	"wednesday": 3,
	"thursday":  4,
	"friday":    5,
	"saturday":  6,
}

// DayInWeek returns the date of a named weekday ("monday", any case) inside
// the 7-day week starting at weekStart, whichever weekday that week starts on.
//
// Relative to weekStart's own weekday rather than "sunday is day 0": with
// WEEK_START_DAY=monday the week runs Monday to Sunday, and counting from
// Sunday would put every meal one day late and Sunday's on the Monday.
func DayInWeek(weekStart time.Time, day string) (time.Time, bool) {
	n, ok := dayOffset[strings.ToLower(strings.TrimSpace(day))]
	if !ok {
		return time.Time{}, false
	}
	return weekStart.AddDate(0, 0, (n-int(weekStart.Weekday())+7)%7), true
}

// daysFrom returns the lowercase day names from fromDate (inclusive) through
// the end of weekStart's week (6 days after weekStart), in that week's own
// order, so a mid-week generation can ask the LLM/Validate for only the days
// that haven't happened yet instead of the full 7. fromDate is clamped into
// [weekStart, weekStart+6]; a fromDate on or before weekStart (the normal,
// non-mid-week case) yields all 7 days.
func daysFrom(weekStart, fromDate time.Time) []string {
	weekStart = weekStart.Truncate(24 * time.Hour)
	fromDate = fromDate.Truncate(24 * time.Hour)
	offset := int(fromDate.Sub(weekStart).Hours() / 24)
	if offset < 0 {
		offset = 0
	}
	if offset > 6 {
		offset = 6
	}
	days := make([]string, 0, 7-offset)
	for i := offset; i < 7; i++ {
		days = append(days, strings.ToLower(weekStart.AddDate(0, 0, i).Weekday().String()))
	}
	return days
}

// Pricer is called after meals are persisted to price the full plan (§6.4).
// Passing nil skips costing (useful in tests).
type Pricer func(ctx context.Context, planID int64, hh *db.Household) error

// GenerateForWeek resolves preferences, calls the LLM, validates the result,
// persists it to the DB, optionally prices it, and returns the new plan ID
// for one week - the generate dialog's week, or the one the auto-plan
// scheduler picked. weekStart is used exactly as given, on whichever weekday
// WEEK_START_DAY puts it (callers pass a real boundary from plan.WeekBounds,
// the same helper the calendar uses); meals are placed by weekday name within
// it (DayInWeek).
// fromDate restricts generation to fromDate through the end of that week -
// the "just the remaining days" choice offered when regenerating mid-week, so
// a day that has already happened is never asked of the LLM (and never
// billed for). Pass weekStart itself (or any date on/before it) for the
// normal full-week case. requested carries this week's specific "make sure
// to include" asks - each entry either a picked recipe (rendered with its
// full ingredients/steps so the LLM reproduces it rather than reinventing
// it) or free text the household typed - collected fresh by the caller
// (handlePlanGenerate) rather than pulled from standing preferences. onHand
// is the "already in the fridge or pantry" list typed on the same form - food
// the plan should use up before buying more. Both are nil for the auto-plan
// scheduler and any other caller with nothing to ask. req is the form those
// two were built from, saved on the plan as submitted so it can be shown later
// and sent again for another week; nil saves nothing.
func GenerateForWeek(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, weekStart, fromDate time.Time, pricer Pricer, checker PriceChecker, j *Job, requested, onHand []string, req *db.PlanRequest) (int64, error) {
	return generate(ctx, store, gen, householdID, weekStart, fromDate, pricer, checker, j, requested, onHand, req)
}

// generate is the implementation behind GenerateForWeek: resolves preferences, calls the LLM, validates the result, persists it to
// the DB, optionally prices it, and returns the new plan ID. j is an optional
// progress sink (nil is fine, e.g. in tests) - it emits a status update at
// each real stage so the progress screen reflects what's actually happening
// instead of sitting on "asking the AI" through pricing and budget repair,
// which can run long after the LLM has already answered. See GenerateForWeek
// for fromDate.
func generate(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, weekStart, fromDate time.Time, pricer Pricer, checker PriceChecker, j *Job, requested, onHand []string, req *db.PlanRequest) (int64, error) {
	hh, err := store.GetHousehold(ctx, householdID)
	if err != nil {
		return 0, fmt.Errorf("get household: %w", err)
	}
	if hh == nil {
		return 0, fmt.Errorf("household not configured")
	}

	profile, err := Resolve(ctx, store, householdID)
	if err != nil {
		return 0, fmt.Errorf("resolve preferences: %w", err)
	}

	stores, err := store.ListStores(ctx, householdID)
	if err != nil {
		return 0, fmt.Errorf("list stores: %w", err)
	}

	weekEnd := weekStart.AddDate(0, 0, 6)
	days := daysFrom(weekStart, fromDate)

	sysPmt, userPmt := BuildPrompt(hh, profile, stores, weekStart, weekEnd, requested, onHand, days)

	plan, err := store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: householdID,
		WeekStart:   weekStart.Format("2006-01-02"),
		WeekEnd:     weekEnd.Format("2006-01-02"),
		BudgetCents: hh.WeeklyBudgetCents,
	})
	if err != nil {
		return 0, fmt.Errorf("create plan: %w", err)
	}

	// The prompt asks the model to use this food up, and it lists it as normal
	// ingredients - kept on the plan so the shopping list can mark those lines
	// already-have instead of buying them again (pricing.ApplyOnHand).
	// Non-fatal: the plan is still worth generating without it.
	if len(onHand) > 0 {
		if err := store.SetPlanOnHand(ctx, plan.ID, onHand); err != nil {
			log.Printf("plan: save on-hand list for plan %d: %v", plan.ID, err)
		}
	}

	// Non-fatal for the same reason: losing "what did I ask for" is not worth
	// losing the plan over.
	if req != nil {
		if err := store.SetPlanRequest(ctx, plan.ID, *req); err != nil {
			log.Printf("plan: save request for plan %d: %v", plan.ID, err)
		}
	}

	// Retire whatever plan(s) this household already had for this week the
	// moment generation actually starts (right after the user confirms the
	// "this replaces your current plan" dialog), not only once the new one
	// succeeds: GetLatestPlan/GetPlanByWeekStart already pick whichever plan
	// row is newest regardless of status, so the old plan stops being "the"
	// plan for this week the instant this row exists either way - leaving it
	// uncanceled until success just left that fact unrecorded. Canceled plans
	// are kept, not deleted, for /plan/history.
	if err := store.CancelOtherPlansForWeek(ctx, householdID, plan.WeekStart, plan.ID); err != nil {
		fmt.Printf("warning: cancel superseded plans failed: %v\n", err)
	}

	st := &genState{
		store:       store,
		householdID: householdID,
		hh:          hh,
		profile:     profile,
		stores:      stores,
		weekStart:   weekStart,
		days:        days,
		planID:      plan.ID,
		pricer:      pricer,
		gc:          &genToolCtx{store: store, householdID: householdID, checker: checker},
		loop:        newGenLoop(sysPmt, userPmt, llm.PlanMaxTokens(gen)),
	}
	return st.run(ctx, gen, j)
}

// genState is everything one generation needs after its prompt is built -
// kept together so a reply cut off at the token limit can be resumed (see
// TruncatedError and ResumeGeneration) without resolving preferences,
// creating another plan row, or re-running any tool lookup.
type genState struct {
	store       db.Store
	householdID int64
	hh          *db.Household
	profile     *PreferenceProfile
	stores      []*db.GroceryStore
	weekStart   time.Time
	days        []string
	planID      int64
	pricer      Pricer
	gc          *genToolCtx
	loop        *genLoop
}

// TruncatedError is returned by generation when the model's reply hit its
// output-token limit. It holds the whole in-flight generation in memory, so
// ResumeGeneration can send the very same request again with a bigger budget
// and carry on from there.
type TruncatedError struct {
	PlanID    int64
	MaxTokens int // the budget the cut-off reply had
	st        *genState
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("parse llm response: response was cut off before completing (max %d output tokens) - try again with a higher limit", e.MaxTokens)
}

// ResumeGeneration re-sends the request that was cut off in te with
// maxTokens as its new output budget, then finishes the generation exactly as
// the first attempt would have. Returns another *TruncatedError if the bigger
// budget still wasn't enough.
func ResumeGeneration(ctx context.Context, gen llm.Generator, te *TruncatedError, maxTokens int, j *Job) (int64, error) {
	st := te.st
	if err := st.store.UpdatePlanStatus(ctx, st.planID, "generating"); err != nil {
		return st.planID, fmt.Errorf("update plan status: %w", err)
	}
	st.loop.maxTokens = maxTokens
	return st.run(ctx, gen, j)
}

// run drives the tool loop from wherever st.loop left off, then validates,
// persists, and (in the background) prices the result.
func (st *genState) run(ctx context.Context, gen llm.Generator, j *Job) (int64, error) {
	store, planID := st.store, st.planID
	ctx = llm.WithPurpose(llm.WithHousehold(ctx, st.householdID), "plan")

	resp, err := runGenerationLoop(ctx, gen, st.loop, st.gc, j)
	if errors.Is(err, errLoopTruncated) {
		_ = store.UpdatePlanStatus(ctx, planID, "error")
		return planID, &TruncatedError{PlanID: planID, MaxTokens: st.loop.maxTokens, st: st}
	}
	if err != nil {
		_ = store.UpdatePlanStatus(ctx, planID, "error")
		return planID, fmt.Errorf("llm generate: %w", err)
	}

	j.EmitStatus("Got a plan back - checking it over…")

	var gp GeneratedPlan
	raw := stripFences(strings.TrimSpace(resp.Content))
	if err := json.Unmarshal([]byte(raw), &gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, planID, "error")
		// Some OpenAI-compatible servers don't report finish_reason
		// reliably; a reply that stops mid-object is still a cut-off one.
		if !strings.HasSuffix(raw, "}") {
			return planID, &TruncatedError{PlanID: planID, MaxTokens: st.loop.maxTokens, st: st}
		}
		return planID, fmt.Errorf("parse llm response: %w", err)
	}

	CleanMealTitles(gp)

	if err := Validate(gp, st.profile, st.days); err != nil {
		_ = store.UpdatePlanStatus(ctx, planID, "error")
		return planID, fmt.Errorf("validate plan: %w", err)
	}

	// Pull any physically absurd ingredient quantity back to a sane cap before
	// it reaches the recipe and the shopping list. Non-fatal by design - see
	// ClampQuantities.
	ClampQuantities(gp)

	// Meals point at the ai_runs row of the call that actually produced them.
	var aiRunID *int64
	if resp.RunID != 0 {
		aiRunID = &resp.RunID
	}

	j.EmitStatus("Saving your meals and recipes…")
	if err := persistPlan(ctx, store, st.householdID, planID, aiRunID, st.weekStart, gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, planID, "error")
		return planID, fmt.Errorf("persist plan: %w", err)
	}

	// Seed one plan_days row per day with everyone eating. The plan page used
	// to fall back to household size for days with no row, which worked for
	// display but left nothing to scale against - now every day has an
	// explicit portion total from the moment the plan exists, and changing who
	// is eating rescales that day's meals.
	seedPlanDays(ctx, store, planID, st.weekStart, st.hh.HouseholdSize, st.profile.Members)

	// Mark leftover slots based on cooked-portions surplus (§5.6).
	if err := PlanLeftovers(ctx, store, planID, st.profile.LeftoverTolerance); err != nil {
		log.Printf("plan: leftover planning failed: %v", err)
	}

	j.EmitStatus("Finishing up…")

	// The plan itself - meals, recipes, leftovers - is fully built and
	// persisted at this point. Pricing is what actually takes real time (a
	// live scrape or AI price lookup per ingredient), so it no longer holds up
	// the redirect to /plan: the plan is marked ready now, and pricing keeps
	// running in the background. The shopping list tab shows skeleton rows
	// (pricing.SeedShoppingList seeds them "pending" before resolving each
	// one) until it catches up - see priceInBackground.
	//
	// Detach from ctx for the status update: the plan is fully persisted here,
	// so a deadline blown later during background pricing must not strand
	// this row in "generating" and leave the progress screen spinning.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	err = store.UpdatePlanStatus(finishCtx, planID, "ready")
	cancel()
	if err != nil {
		return planID, fmt.Errorf("update plan status: %w", err)
	}

	if st.pricer != nil {
		go priceInBackground(store, gen, planID, st.hh, st.profile, st.stores, st.pricer)
	} else {
		// No pricer configured (tests, or no pricing chain) - nothing to
		// background; write an unpriced list synchronously so the plan still
		// has something to shop from.
		listCtx, listCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if _, lerr := pricing.EnsureShoppingList(listCtx, store, planID, st.hh); lerr != nil {
			log.Printf("plan: shopping list fallback failed for plan %d: %v", planID, lerr)
		}
		listCancel()
	}

	return planID, nil
}

// priceInBackground runs pricing, budget repair, and the unpriced-list
// fallback after generate has already marked the plan ready and returned -
// see the comment above its call site. It uses its own fully detached
// context/budget: by the time this runs, the job that kicked off generation
// may already have reported "done" and torn down its own context, and this
// still has real network work ahead of it (a scrape or AI lookup per
// ingredient, then up to 3 repair rounds that each re-price the whole list).
func priceInBackground(store db.Store, gen llm.Generator, planID int64, hh *db.Household, profile *PreferenceProfile, stores []*db.GroceryStore, pricer Pricer) {
	ctx, cancel := context.WithTimeout(llm.WithHousehold(context.Background(), hh.ID), 15*time.Minute)
	defer cancel()

	// Lets the shopping list's "stop pricing" button abort this run early -
	// see StopPricing.
	RegisterPricingCancel(planID, cancel)
	defer ClearPricingCancel(planID)

	if err := pricer(ctx, planID, hh); err != nil {
		log.Printf("plan: costing failed for plan %d: %v", planID, err)
	}
	// Budget repair loop (§7.6): attempt up to 3 swaps if over budget. No job
	// to report progress to any more - the generation SSE stream already
	// closed once the plan was marked ready.
	if _, err := Repair(ctx, store, gen, planID, hh, profile, stores, pricer, 3, nil); err != nil {
		log.Printf("plan: budget repair failed for plan %d: %v", planID, err)
	}

	// A plan without a shopping list is not a finished plan. Pricing is
	// deliberately non-fatal on failure, which could otherwise leave a ready
	// plan with nothing to shop.
	listCtx, listCancel := context.WithTimeout(ctx, 30*time.Second)
	defer listCancel()
	n, lerr := pricing.EnsureShoppingList(listCtx, store, planID, hh)
	if lerr != nil {
		log.Printf("plan: shopping list fallback failed for plan %d: %v", planID, lerr)
	} else if n > 0 {
		log.Printf("plan: wrote %d unpriced shopping lines for plan %d (pricing produced none)", n, planID)
	}
}

// itemHintFrom carries the model's per-ingredient unit knowledge (the stock
// unit it should be bought in, plus any odd conversions like "1 clove = 5 g")
// into catalog item creation, so a new item lands with the right unit and a
// full conversion table rather than defaulting to "each" with none.
func itemHintFrom(ing GeneratedIngredient) catalog.ItemHint {
	h := catalog.ItemHint{Unit: ing.ItemUnit}
	for _, c := range ing.Conversions {
		h.Conversions = append(h.Conversions, catalog.UnitEdge{From: c.From, To: c.To, Factor: c.Factor})
	}
	return h
}

// persistPlan writes a generated plan to the database: the meals, their steps,
// and their ingredients - each linked to a catalog item as it is created - and
// saves every meal to the household's recipe catalog.
//
// Linking happens here rather than only in the pricer, which is where it used
// to live: buildPricer returns nil when no store chain is configured, so a
// household with no stores never linked a single ingredient and every shopping
// line stayed unmatched forever.
func persistPlan(ctx context.Context, store db.Store, householdID, planID int64, aiRunID *int64, weekStart time.Time, gp GeneratedPlan) error {
	for _, gm := range gp.Meals {
		day, ok := DayInWeek(weekStart, gm.Day)
		if !ok {
			return fmt.Errorf("unknown day %q", gm.Day)
		}
		mealDate := day.Format("2006-01-02")

		meal, err := store.CreateMeal(ctx, db.CreateMealParams{
			PlanID:         planID,
			Day:            mealDate,
			Slot:           gm.Slot,
			Title:          gm.Title,
			Effort:         gm.Effort,
			Servings:       gm.Servings,
			CookedPortions: gm.CookedPortions,
			AIRunID:        aiRunID,
		})
		if err != nil {
			return fmt.Errorf("create meal %s %s: %w", gm.Day, gm.Slot, err)
		}

		stepsJSON, err := json.Marshal(gm.Steps)
		if err != nil {
			return err
		}
		if err := store.CreateMealRecipe(ctx, db.CreateMealRecipeParams{
			MealID:    meal.ID,
			StepsJSON: string(stepsJSON),
			Servings:  gm.Servings,
		}); err != nil {
			return fmt.Errorf("create recipe for meal %d: %w", meal.ID, err)
		}

		for _, ing := range gm.Ingredients {
			// Resolve the name to a catalog item now - EnsureItem tries an
			// exact term, then the household's aliases, then a confident fuzzy
			// match, and only creates a placeholder as a last resort. A
			// failure here is non-fatal: an unlinked ingredient still shops,
			// it just shows as unmatched on the list.
			var itemID *int64
			term := pricing.Normalize(ing.Name)
			if it, ierr := catalog.EnsureItemWithHint(ctx, store, householdID, ing.Name, itemHintFrom(ing)); ierr != nil {
				log.Printf("plan: link ingredient %q: %v", ing.Name, ierr)
			} else if it != nil {
				itemID = &it.ID
				term = it.NormalizedTerm
			}

			if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
				MealID:         meal.ID,
				Name:           ing.Name,
				Quantity:       ing.Quantity,
				Unit:           ing.Unit,
				NormalizedTerm: term,
				ItemID:         itemID,
				EstPriceCents:  ing.EstPriceCents,
			}); err != nil {
				return fmt.Errorf("create ingredient %q: %w", ing.Name, err)
			}
		}

		// Keep the recipe, not just this week's meal. Non-fatal: a plan that
		// cooks is worth more than a catalog entry, and losing the whole
		// generation over a duplicate title would be absurd.
		if err := saveGeneratedRecipe(ctx, store, householdID, gm); err != nil {
			log.Printf("plan: save recipe %q to catalog: %v", gm.Title, err)
		}
	}
	return nil
}

// saveGeneratedRecipe files one generated meal in the household's recipe
// catalog so it can be cooked again outside this week's plan.
//
// Skips a title the household already has. The same meals come back week after
// week, and twenty copies of "Weeknight Chili" would make /recipes useless;
// the existing entry is left alone rather than overwritten, because it may
// have been edited by hand since.
//
// Skips a leftovers meal too: it eats an earlier meal's surplus, so there is
// no recipe to keep - just the model's placeholder "ingredients" for it.
func saveGeneratedRecipe(ctx context.Context, store db.Store, householdID int64, gm GeneratedMeal) error {
	title := strings.TrimSpace(gm.Title)
	if title == "" || db.IsLeftoverTitle(title) {
		return nil
	}
	existing, err := store.GetCatalogRecipeByTitle(ctx, householdID, title)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}

	recipe, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: householdID,
		Title:       title,
		SourceKind:  "ai",
		Servings:    gm.Servings,
		PrepMinutes: gm.PrepMinutes,
		CookMinutes: gm.CookMinutes,
		Tags:        gm.Tags,
	})
	if err != nil {
		return err
	}

	for i, ing := range gm.Ingredients {
		// Catalog recipes store quantity as free text (that is what an import
		// gives you); FormatQty keeps "1" as "1" and a converted "907.18471..."
		// to two decimals, since this text is what the recipe page shows.
		qty := pricing.FormatQty(ing.Quantity)
		if err := store.AddCatalogRecipeIngredient(ctx, recipe.ID, ing.Name, qty, ing.Unit, i); err != nil {
			return err
		}
	}
	for i, step := range gm.Steps {
		if err := store.AddCatalogRecipeStep(ctx, recipe.ID, i, step); err != nil {
			return err
		}
	}
	return nil
}

// seedPlanDays writes a row for each of the week's seven days, defaulting to
// everyone in the household eating. Failures are logged, not fatal: a missing
// row only means the plan page falls back to household size for that day.
//
// The portion total, not the headcount, is what the day is scaled to - two
// adults and two toddlers is 3.0 portions - so a household with members seeded
// gets the right quantities from the moment the plan exists rather than only
// after someone touches the day's people picker.
func seedPlanDays(ctx context.Context, store db.Store, planID int64, weekStart time.Time, householdSize int, members []*db.HouseholdMember) {
	headcount := len(members)
	if headcount == 0 {
		headcount = householdSize
	}
	if headcount < 1 {
		headcount = 1
	}

	ids := make([]int64, 0, len(members))
	var portions float64
	for _, m := range members {
		ids = append(ids, m.ID)
		portions += m.PortionFactor
	}
	if portions <= 0 {
		portions = float64(headcount)
	}

	for i := 0; i < 7; i++ {
		date := weekStart.AddDate(0, 0, i).Format("2006-01-02")
		if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
			PlanID:    planID,
			Date:      date,
			Headcount: headcount,
			MemberIDs: ids,
			Portions:  portions,
		}); err != nil {
			log.Printf("plan: seed plan day %s: %v", date, err)
		}
	}
}

// stripFences removes optional markdown code fences that some LLMs wrap JSON in.
func stripFences(s string) string {
	// Handle ```json\n...\n``` and ```\n...\n```
	for _, prefix := range []string{"```json", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			s = strings.TrimPrefix(s, "\n")
			s = strings.TrimSuffix(strings.TrimSpace(s), "```")
			s = strings.TrimSpace(s)
			break
		}
	}
	return s
}

func mustLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}
