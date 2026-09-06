package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"goeat/db"
	"goeat/llm"
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

// Pricer is called after meals are persisted to price the full plan (§6.4).
// Passing nil skips costing (useful in tests).
type Pricer func(ctx context.Context, planID int64, hh *db.Household) error

// Generate resolves preferences, calls the LLM, validates the result, persists
// it to the DB, optionally prices it, and returns the new plan ID.
func Generate(ctx context.Context, store db.Store, gen llm.Generator, householdID int64, pricer Pricer) (int64, error) {
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

	weekStart := nextSunday(time.Now().In(mustLocation(hh.Timezone)))
	weekEnd := weekStart.AddDate(0, 0, 6)

	sysPmt, userPmt := BuildPrompt(hh, profile, weekStart, weekEnd)

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
		HouseholdID:  householdID,
		WeekStart:    weekStart.Format("2006-01-02"),
		WeekEnd:      weekEnd.Format("2006-01-02"),
		BudgetCents:  hh.WeeklyBudgetCents,
	})
	if err != nil {
		return 0, fmt.Errorf("create plan: %w", err)
	}

	resp, err := gen.Generate(ctx, llm.GenerateRequest{
		System:    sysPmt,
		Prompt:    userPmt,
		MaxTokens: 8192,
	})
	if err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("llm generate: %w", err)
	}

	var gp GeneratedPlan
	raw := strings.TrimSpace(resp.Content)
	if err := json.Unmarshal([]byte(raw), &gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("parse llm response: %w", err)
	}

	if err := Validate(gp, profile); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("validate plan: %w", err)
	}

	if err := persistPlan(ctx, store, plan.ID, aiRun.ID, weekStart, gp); err != nil {
		_ = store.UpdatePlanStatus(ctx, plan.ID, "error")
		return plan.ID, fmt.Errorf("persist plan: %w", err)
	}

	// Mark leftover slots based on cooked-portions surplus (§5.6).
	if err := PlanLeftovers(ctx, store, plan.ID, profile.LeftoverTolerance); err != nil {
		fmt.Printf("warning: leftover planning failed: %v\n", err)
	}

	// Price the plan when a pricer is provided. Failure is non-fatal.
	if pricer != nil {
		if err := pricer(ctx, plan.ID, hh); err != nil {
			fmt.Printf("warning: plan costing failed: %v\n", err)
		}
		// Budget repair loop (§7.6): attempt up to 3 swaps if over budget.
		if _, err := Repair(ctx, store, gen, plan.ID, hh, profile, pricer, 3); err != nil {
			fmt.Printf("warning: budget repair failed: %v\n", err)
		}
	}

	if err := store.UpdatePlanStatus(ctx, plan.ID, "ready"); err != nil {
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
				MealID:   meal.ID,
				Name:     ing.Name,
				Quantity: ing.Quantity,
				Unit:     ing.Unit,
			}); err != nil {
				return fmt.Errorf("create ingredient %q: %w", ing.Name, err)
			}
		}
	}
	return nil
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
