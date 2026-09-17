package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"goeat/catalog"
	"goeat/db"
	"goeat/llm"
	"goeat/pricing"
)

// planGenMaxTokens caps the LLM response for a full week's plan. Bumped from
// 8192 after real responses were getting cut off mid-JSON on busy weeks.
const planGenMaxTokens = 16384

var dayOffset = map[string]int{
	"sunday":    0,
	"monday":    1,
	"tuesday":   2,
	"wednesday": 3,
	"thursday":  4,
	"friday":    5,
	"saturday":  6,
}

var dayNameByOffset = [7]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

// daysFrom returns the lowercase day names from fromDate (inclusive) through
// the end of weekStart's week (6 days after weekStart), so a mid-week
// generation can ask the LLM/Validate for only the days that haven't
// happened yet instead of the full 7. fromDate is clamped into
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
	return append([]string(nil), dayNameByOffset[offset:]...)
}

// Pricer is called after meals are persisted to price the full plan (§6.4).
// Passing nil skips costing (useful in tests).
type Pricer func(ctx context.Context, planID int64, hh *db.Household) error

// Generate resolves preferences, calls the LLM, validates the result, persists
// it to the DB, optionally prices it, and returns the new plan ID for the
// upcoming week (today's date rolled forward to the next Sunday - today
// itself, if today is Sunday). This is what the auto-plan scheduler calls;
// the dashboard's "Plan my week" / "Regenerate" buttons go through
// GenerateForWeek with an explicit week (the one containing today). See
// GenerateForWeek for a specific past or future week.
func Generate(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, pricer Pricer, checker PriceChecker, j *Job) (int64, error) {
	hh, err := store.GetHousehold(ctx)
	if err != nil {
		return 0, fmt.Errorf("get household: %w", err)
	}
	if hh == nil {
		return 0, fmt.Errorf("household not configured")
	}
	weekStart := nextSunday(time.Now().In(mustLocation(hh.Timezone)))
	return generate(ctx, store, gen, householdID, weekStart, weekStart, pricer, checker, j, nil)
}

// GenerateForWeek is Generate for one specific week rather than "whichever
// week is next" - the dashboard's manual "generate"/"regenerate" action for a
// past or future week, reached by navigating the calendar widget away from
// the current one. weekStart need not be a Sunday; it is used exactly as
// given (the caller - handlePlanGenerate - is expected to pass a real week
// boundary from plan.WeekBounds, the same helper the calendar itself uses).
// fromDate restricts generation to fromDate through the end of that week -
// the "just the remaining days" choice offered when regenerating mid-week, so
// a day that has already happened is never asked of the LLM (and never
// billed for). Pass weekStart itself (or any date on/before it) for the
// normal full-week case. requested carries this week's specific "make sure
// to include" asks - each entry either a picked recipe (rendered with its
// full ingredients/steps so the LLM reproduces it rather than reinventing
// it) or free text the household typed - collected fresh by the caller
// (handlePlanGenerate) rather than pulled from standing preferences. nil for
// the auto-plan scheduler and any other caller with nothing to ask.
func GenerateForWeek(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, weekStart, fromDate time.Time, pricer Pricer, checker PriceChecker, j *Job, requested []string) (int64, error) {
	return generate(ctx, store, gen, householdID, weekStart, fromDate, pricer, checker, j, requested)
}

// generate is the shared implementation behind Generate and GenerateForWeek:
// resolves preferences, calls the LLM, validates the result, persists it to
// the DB, optionally prices it, and returns the new plan ID. j is an optional
// progress sink (nil is fine, e.g. in tests) - it emits a status update at
// each real stage so the progress screen reflects what's actually happening
// instead of sitting on "asking the AI" through pricing and budget repair,
// which can run long after the LLM has already answered. See GenerateForWeek
// for fromDate.
func generate(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, weekStart, fromDate time.Time, pricer Pricer, checker PriceChecker, j *Job, requested []string) (int64, error) {
	hh, err := store.GetHousehold(ctx)
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

	sysPmt, userPmt := BuildPrompt(hh, profile, stores, weekStart, weekEnd, requested, days)

	aiRun, err := store.CreateAIRun(ctx, db.CreateAIRunParams{
		HouseholdID: householdID,
		Purpose:     "plan",
		Provider:    gen.ProviderName(),
		Model:       gen.ModelName(),
		Status:      "running",
	})
	if err != nil {
		return 0, fmt.Errorf("create ai_run: %w", err)
	}

	plan, err := store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: householdID,
		WeekStart:   weekStart.Format("2006-01-02"),
		WeekEnd:     weekEnd.Format("2006-01-02"),
		BudgetCents: hh.WeeklyBudgetCents,
	})
	if err != nil {
		return 0, fmt.Errorf("create plan: %w", err)
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

	gc := &genToolCtx{store: store, householdID: householdID, checker: checker}
	rawResp, err := runGenerationLoop(ctx, gen, sysPmt, userPmt, gc, j)
	if err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("llm generate: %w", err)
	}

	j.EmitStatus("Got a plan back - checking it over…")

	var gp GeneratedPlan
	raw := stripFences(strings.TrimSpace(rawResp))
	if err := json.Unmarshal([]byte(raw), &gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		if !strings.HasSuffix(raw, "}") {
			return plan.ID, fmt.Errorf("parse llm response: response was cut off before completing (max %d output tokens) - raise the token limit or shorten the plan: %w", planGenMaxTokens, err)
		}
		return plan.ID, fmt.Errorf("parse llm response: %w", err)
	}

	if err := Validate(gp, profile, days); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("validate plan: %w", err)
	}

	// Pull any physically absurd ingredient quantity back to a sane cap before
	// it reaches the recipe and the shopping list. Non-fatal by design - see
	// ClampQuantities.
	ClampQuantities(gp)

	j.EmitStatus("Saving your meals and recipes…")
	if err := persistPlan(ctx, store, householdID, plan.ID, aiRun.ID, weekStart, gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("persist plan: %w", err)
	}

	// Seed one plan_days row per day with everyone eating. The plan page used
	// to fall back to household size for days with no row, which worked for
	// display but left nothing to scale against - now every day has an
	// explicit portion total from the moment the plan exists, and changing who
	// is eating rescales that day's meals.
	seedPlanDays(ctx, store, plan.ID, weekStart, hh.HouseholdSize, profile.Members)

	// Mark leftover slots based on cooked-portions surplus (§5.6).
	if err := PlanLeftovers(ctx, store, plan.ID, profile.LeftoverTolerance); err != nil {
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
	err = store.UpdatePlanStatus(finishCtx, plan.ID, "ready")
	cancel()
	if err != nil {
		return plan.ID, fmt.Errorf("update plan status: %w", err)
	}

	if pricer != nil {
		go priceInBackground(store, gen, plan.ID, hh, profile, stores, pricer)
	} else {
		// No pricer configured (tests, or no pricing chain) - nothing to
		// background; write an unpriced list synchronously so the plan still
		// has something to shop from.
		listCtx, listCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if _, lerr := pricing.EnsureShoppingList(listCtx, store, plan.ID, hh); lerr != nil {
			log.Printf("plan: shopping list fallback failed for plan %d: %v", plan.ID, lerr)
		}
		listCancel()
	}

	return plan.ID, nil
}

// priceInBackground runs pricing, budget repair, and the unpriced-list
// fallback after generate has already marked the plan ready and returned -
// see the comment above its call site. It uses its own fully detached
// context/budget: by the time this runs, the job that kicked off generation
// may already have reported "done" and torn down its own context, and this
// still has real network work ahead of it (a scrape or AI lookup per
// ingredient, then up to 3 repair rounds that each re-price the whole list).
func priceInBackground(store db.Store, gen llm.Generator, planID int64, hh *db.Household, profile *PreferenceProfile, stores []*db.GroceryStore, pricer Pricer) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
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
func persistPlan(ctx context.Context, store db.Store, householdID, planID, aiRunID int64, weekStart time.Time, gp GeneratedPlan) error {
	for _, gm := range gp.Meals {
		day := strings.ToLower(gm.Day)
		offset, ok := dayOffset[day]
		if !ok {
			return fmt.Errorf("unknown day %q", gm.Day)
		}
		mealDate := weekStart.AddDate(0, 0, offset).Format("2006-01-02")

		meal, err := store.CreateMeal(ctx, db.CreateMealParams{
			PlanID:         planID,
			Day:            mealDate,
			Slot:           gm.Slot,
			Title:          gm.Title,
			Effort:         gm.Effort,
			Servings:       gm.Servings,
			CookedPortions: gm.CookedPortions,
			AIRunID:        &aiRunID,
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
func saveGeneratedRecipe(ctx context.Context, store db.Store, householdID int64, gm GeneratedMeal) error {
	title := strings.TrimSpace(gm.Title)
	if title == "" {
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
		// gives you); %g keeps "1" as "1" rather than "1.0000".
		qty := strconv.FormatFloat(ing.Quantity, 'g', -1, 64)
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

// nextSunday returns the upcoming Sunday (or today if today is Sunday).
func nextSunday(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	daysUntil := (7 - int(t.Weekday())) % 7
	return t.AddDate(0, 0, daysUntil)
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
