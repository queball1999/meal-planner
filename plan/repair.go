package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"goeat/db"
	"goeat/llm"
)

// Repair attempts to bring a plan's total under budget by replacing the
// highest-cost unlocked meals. It calls gen up to maxIters times and re-prices
// after each round (§7.6). Returns true when the plan is within budget.
func Repair(
	ctx context.Context,
	store db.Store,
	gen llm.Generator,
	planID int64,
	hh *db.Household,
	profile *PreferenceProfile,
	stores []*db.GroceryStore,
	pricer Pricer,
	maxIters int,
	j *Job,
) (repaired bool, err error) {
	for iter := 0; iter < maxIters; iter++ {
		plan, err := store.GetPlanByID(ctx, planID)
		if err != nil || plan == nil {
			return false, fmt.Errorf("repair: get plan: %w", err)
		}
		if plan.TotalCents <= plan.BudgetCents {
			return true, nil
		}

		meals, err := store.ListMealsByPlan(ctx, planID)
		if err != nil {
			return false, fmt.Errorf("repair: list meals: %w", err)
		}
		items, err := store.ListShoppingListItems(ctx, planID)
		if err != nil {
			return false, fmt.Errorf("repair: list items: %w", err)
		}

		costByMeal := estimateMealCosts(meals, items)
		unlocked := make([]*db.Meal, 0, len(meals))
		for _, m := range meals {
			if !m.Locked && !m.IsLeftover {
				unlocked = append(unlocked, m)
			}
		}
		sort.Slice(unlocked, func(i, j int) bool {
			return costByMeal[unlocked[i].ID] > costByMeal[unlocked[j].ID]
		})

		targets := unlocked
		if len(targets) > 3 {
			targets = targets[:3]
		}
		if len(targets) == 0 {
			break
		}

		weekStart := weekStartFromMeals(meals)
		weekEnd := weekStart.AddDate(0, 0, 6)
		sysPmt, _ := BuildPrompt(hh, profile, stores, weekStart, weekEnd)
		userPmt := buildRepairPrompt(hh, profile, targets, plan.BudgetCents, plan.TotalCents)

		j.EmitStatus(fmt.Sprintf(
			"$%.2f over budget - asking the AI for %d cheaper meal(s) (round %d of %d)…",
			float64(plan.TotalCents-plan.BudgetCents)/100, len(targets), iter+1, maxIters))

		// Reasoning stays on here - swapping a meal is a judgement call, not a
		// lookup - so the budget has to cover the thinking as well as the JSON.
		resp, err := gen.Generate(ctx, llm.GenerateRequest{
			System:    sysPmt,
			Prompt:    userPmt,
			MaxTokens: 8192,
		})
		if err != nil {
			fmt.Printf("repair iter %d: generate: %v\n", iter+1, err)
			break
		}

		var partial struct {
			Meals []GeneratedMeal `json:"meals"`
		}
		raw := strings.TrimSpace(resp.Content)
		if err := json.Unmarshal([]byte(raw), &partial); err != nil {
			fmt.Printf("repair iter %d: parse: %v\n", iter+1, err)
			break
		}

		for _, gm := range partial.Meals {
			if err := validateSingleMeal(gm, profile); err != nil {
				fmt.Printf("repair iter %d: validation skip %q: %v\n", iter+1, gm.Title, err)
				continue
			}
			if err := replaceMeal(ctx, store, meals, gm); err != nil {
				fmt.Printf("repair iter %d: replace meal: %v\n", iter+1, err)
			}
		}

		if pricer != nil {
			j.EmitStatus(fmt.Sprintf("Re-pricing after round %d swaps…", iter+1))
			if err := pricer(ctx, planID, hh); err != nil {
				fmt.Printf("repair iter %d: pricer: %v\n", iter+1, err)
			}
		}
	}

	plan, _ := store.GetPlanByID(ctx, planID)
	if plan != nil && plan.TotalCents <= plan.BudgetCents {
		return true, nil
	}
	return false, nil
}

func buildRepairPrompt(hh *db.Household, profile *PreferenceProfile, targets []*db.Meal, budgetCents, totalCents int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The current meal plan costs $%.2f but the budget is $%.2f.\n",
		float64(totalCents)/100, float64(budgetCents)/100)
	b.WriteString("Replace ONLY these meals with cheaper alternatives that honor the same constraints:\n\n")
	for _, m := range targets {
		fmt.Fprintf(&b, "- %s %s: %q\n", weekdayName(m.Day), m.Slot, m.Title)
	}
	b.WriteString("\nReturn ONLY a JSON object with a \"meals\" array containing the replacement meals.\n")
	b.WriteString("Each replacement must include: day, slot, title, effort, servings, cooked_portions, ingredients, steps.\n")
	b.WriteString("Keep the same day+slot as the meal being replaced.")
	if len(profile.Allergies) > 0 {
		fmt.Fprintf(&b, "\nNever include: %s", strings.Join(profile.Allergies, ", "))
	}
	if len(profile.DietTags) > 0 {
		fmt.Fprintf(&b, "\nDiet constraints: %s", strings.Join(profile.DietTags, ", "))
	}
	fmt.Fprintf(&b, "\nHousehold size: %d. Budget target per meal: $%.2f.",
		hh.HouseholdSize, float64(budgetCents)/100/21)
	return b.String()
}

// replaceMeal updates one meal DB row to match the replacement and rewrites its
// recipe and ingredients. Identified by matching day+slot in the existing meals list.
func replaceMeal(ctx context.Context, store db.Store, meals []*db.Meal, gm GeneratedMeal) error {
	day := strings.ToLower(gm.Day)
	slot := strings.ToLower(gm.Slot)

	var target *db.Meal
	for _, m := range meals {
		if weekdayName(m.Day) == day && m.Slot == slot {
			target = m
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no meal for %s %s", day, slot)
	}

	if err := store.UpdateMealTitle(ctx, target.ID, gm.Title, gm.Effort, gm.Servings, gm.CookedPortions); err != nil {
		return fmt.Errorf("update meal title: %w", err)
	}

	stepsJSON, _ := json.Marshal(gm.Steps)
	if err := store.CreateMealRecipe(ctx, db.CreateMealRecipeParams{
		MealID:    target.ID,
		StepsJSON: string(stepsJSON),
		Servings:  gm.Servings,
	}); err != nil {
		return fmt.Errorf("update recipe: %w", err)
	}

	if err := store.DeleteMealIngredients(ctx, target.ID); err != nil {
		return fmt.Errorf("delete old ingredients: %w", err)
	}
	for _, ing := range gm.Ingredients {
		if err := store.CreateMealIngredient(ctx, db.CreateMealIngredientParams{
			MealID:        target.ID,
			Name:          ing.Name,
			Quantity:      ing.Quantity,
			Unit:          ing.Unit,
			EstPriceCents: ing.EstPriceCents,
		}); err != nil {
			return fmt.Errorf("create ingredient: %w", err)
		}
	}
	return nil
}

// estimateMealCosts distributes shopping list cost equally across unlocked meals
// as an approximation for ranking which meals to swap.
func estimateMealCosts(meals []*db.Meal, items []*db.ShoppingListItem) map[int64]int64 {
	costs := make(map[int64]int64, len(meals))
	if len(meals) == 0 {
		return costs
	}
	var total int64
	for _, item := range items {
		total += item.LineTotalCents
	}
	avg := total / int64(len(meals))
	for _, m := range meals {
		costs[m.ID] = avg
	}
	return costs
}

func weekStartFromMeals(meals []*db.Meal) time.Time {
	if len(meals) == 0 {
		return nextSunday(time.Now())
	}
	earliest := meals[0].Day
	for _, m := range meals[1:] {
		if m.Day < earliest {
			earliest = m.Day
		}
	}
	t, err := time.Parse("2006-01-02", earliest)
	if err != nil {
		return nextSunday(time.Now())
	}
	// Walk back to Sunday.
	for t.Weekday() != time.Sunday {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

func weekdayName(isoDate string) string {
	t, err := time.Parse("2006-01-02", isoDate)
	if err != nil {
		return ""
	}
	return strings.ToLower(t.Weekday().String())
}

func validateSingleMeal(gm GeneratedMeal, profile *PreferenceProfile) error {
	allergySet := toLowerSet(profile.Allergies)
	dietRules := dietIngredientRules(profile.DietTags)
	for _, ing := range gm.Ingredients {
		if strings.TrimSpace(ing.Unit) == "" {
			return fmt.Errorf("ingredient %q is missing a unit", ing.Name)
		}
		// See validate.go's identical check: 0 is a legitimate "to taste"/
		// garnish quantity, only negative is a real error.
		if ing.Quantity < 0 {
			return fmt.Errorf("ingredient %q has a negative quantity", ing.Name)
		}
		nameLower := strings.ToLower(ing.Name)
		for allergen := range allergySet {
			if strings.Contains(nameLower, allergen) {
				return fmt.Errorf("allergen %q in %q", allergen, ing.Name)
			}
		}
		for _, rule := range dietRules {
			if rule(nameLower) {
				return fmt.Errorf("diet violation: %q", ing.Name)
			}
		}
	}
	return nil
}
