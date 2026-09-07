package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

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

// Pricer is called after meals are persisted to price the full plan (§6.4).
// Passing nil skips costing (useful in tests).
type Pricer func(ctx context.Context, planID int64, hh *db.Household) error

// Generate resolves preferences, calls the LLM, validates the result, persists
// it to the DB, optionally prices it, and returns the new plan ID. j is an
// optional progress sink (nil is fine, e.g. in tests) - Generate emits a
// status update at each real stage so the progress screen reflects what's
// actually happening instead of sitting on "asking the AI" through pricing
// and budget repair, which can run long after the LLM has already answered.
func Generate(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, pricer Pricer, j *Job) (int64, error) {
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

	weekStart := nextSunday(time.Now().In(mustLocation(hh.Timezone)))
	weekEnd := weekStart.AddDate(0, 0, 6)

	sysPmt, userPmt := BuildPrompt(hh, profile, stores, weekStart, weekEnd)

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

	resp, err := gen.Generate(ctx, llm.GenerateRequest{
		System:    sysPmt,
		Prompt:    userPmt,
		MaxTokens: planGenMaxTokens,
	})
	if err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("llm generate: %w", err)
	}

	j.EmitStatus("Got a plan back - checking it over…")

	var gp GeneratedPlan
	raw := stripFences(strings.TrimSpace(resp.Content))
	if err := json.Unmarshal([]byte(raw), &gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		if !strings.HasSuffix(raw, "}") {
			return plan.ID, fmt.Errorf("parse llm response: response was cut off before completing (used %d/%d output tokens) - raise the token limit or shorten the plan: %w", resp.OutputTokens, planGenMaxTokens, err)
		}
		return plan.ID, fmt.Errorf("parse llm response: %w", err)
	}

	if err := Validate(gp, profile); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("validate plan: %w", err)
	}

	j.EmitStatus("Saving your meals and recipes…")
	if err := persistPlan(ctx, store, plan.ID, aiRun.ID, weekStart, gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("persist plan: %w", err)
	}

	// Seed one plan_days row per day at the household's own size. The plan
	// page used to fall back to household size for days with no row, which
	// worked for display but left nothing to scale against - now every day has
	// an explicit headcount from the moment the plan exists, and changing one
	// rescales that day's meals.
	seedPlanDays(ctx, store, plan.ID, weekStart, hh.HouseholdSize)

	// Mark leftover slots based on cooked-portions surplus (§5.6).
	if err := PlanLeftovers(ctx, store, plan.ID, profile.LeftoverTolerance); err != nil {
		log.Printf("plan: leftover planning failed: %v", err)
	}

	// Price the plan when a pricer is provided. Failure is non-fatal.
	if pricer != nil {
		j.EmitStatus("Pricing your shopping list… (checking store prices for each ingredient)")
		if err := pricer(ctx, plan.ID, hh); err != nil {
			log.Printf("plan: costing failed for plan %d: %v", plan.ID, err)
		}
		// Budget repair loop (§7.6): attempt up to 3 swaps if over budget.
		if _, err := Repair(ctx, store, gen, plan.ID, hh, profile, stores, pricer, 3, j); err != nil {
			log.Printf("plan: budget repair failed for plan %d: %v", plan.ID, err)
		}
	}

	j.EmitStatus("Building your shopping list…")

	// A plan without a shopping list is not a finished plan. Pricing is
	// optional (no chain is configured in tests, or when the user has no
	// stores) and its failures are deliberately non-fatal, so both paths could
	// previously hand back a finished plan with nothing to shop. Detached from
	// ctx for the same reason the status update below is: the meals are
	// already persisted, and a deadline blown during pricing must not also
	// cost the user their list.
	listCtx, listCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	n, lerr := pricing.EnsureShoppingList(listCtx, store, plan.ID, hh)
	listCancel()
	if lerr != nil {
		log.Printf("plan: shopping list fallback failed for plan %d: %v", plan.ID, lerr)
	} else if n > 0 {
		log.Printf("plan: wrote %d unpriced shopping lines for plan %d (pricing produced none)", n, plan.ID)
	}

	j.EmitStatus("Finishing up…")

	// Detach from ctx: the plan is fully built and persisted here, so a
	// deadline that expired during pricing/repair must not strand the row
	// in "generating" and leave the progress screen spinning.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := store.UpdatePlanStatus(finishCtx, plan.ID, "ready"); err != nil {
		return plan.ID, fmt.Errorf("update plan status: %w", err)
	}

	return plan.ID, nil
}

func persistPlan(ctx context.Context, store db.Store, planID, aiRunID int64, weekStart time.Time, gp GeneratedPlan) error {
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
			if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
				MealID:        meal.ID,
				Name:          ing.Name,
				Quantity:      ing.Quantity,
				Unit:          ing.Unit,
				EstPriceCents: ing.EstPriceCents,
			}); err != nil {
				return fmt.Errorf("create ingredient %q: %w", ing.Name, err)
			}
		}
	}
	return nil
}

// seedPlanDays writes a headcount row for each of the week's seven days.
// Failures are logged, not fatal: a missing row only means the plan page falls
// back to household size for that day.
func seedPlanDays(ctx context.Context, store db.Store, planID int64, weekStart time.Time, householdSize int) {
	if householdSize < 1 {
		householdSize = 1
	}
	for i := 0; i < 7; i++ {
		date := weekStart.AddDate(0, 0, i).Format("2006-01-02")
		if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
			PlanID:    planID,
			Date:      date,
			Headcount: householdSize,
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
