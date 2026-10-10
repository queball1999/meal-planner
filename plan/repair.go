package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

		// The plan's own week, not one inferred from its meals: a week need
		// not start on a Sunday, and its first day need not have a meal.
		weekStart, perr := time.Parse("2006-01-02", plan.WeekStart)
		if perr != nil {
			return false, fmt.Errorf("repair: plan week start %q: %w", plan.WeekStart, perr)
		}
		weekEnd := weekStart.AddDate(0, 0, 6)
		sysPmt, _ := BuildPrompt(hh, profile, stores, weekStart, weekEnd, nil, nil, nil, nil, nil)
		userPmt := buildRepairPrompt(hh, profile, targets, meals, plan.BudgetCents, plan.TotalCents)

		j.EmitStatus(fmt.Sprintf(
			"$%.2f over budget - asking the AI for %d cheaper meal(s) (round %d of %d)…",
			float64(plan.TotalCents-plan.BudgetCents)/100, len(targets), iter+1, maxIters))

		// Reasoning stays on here - swapping a meal is a judgement call, not a
		// lookup - so the budget has to cover the thinking as well as the JSON.
		j.EmitLLMStart()
		resp, err := gen.Generate(llm.WithPurpose(ctx, "repair"), llm.GenerateRequest{
			System:      sysPmt,
			Prompt:      userPmt,
			MaxTokens:   8192,
			OnDelta:     j.EmitDelta,
			OnReasoning: j.EmitReasoning,
		})
		j.EmitLLMEnd()
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
		CleanMealTitles(GeneratedPlan{Meals: partial.Meals})
		if err := expandSavedRecipes(ctx, store, hh.ID, partial.Meals, profile.TotalPortions(hh.HouseholdSize)); err != nil {
			fmt.Printf("repair iter %d: saved recipe: %v\n", iter+1, err)
			break
		}

		for _, gm := range partial.Meals {
			if err := validateSingleMeal(gm, profile); err != nil {
				fmt.Printf("repair iter %d: validation skip %q: %v\n", iter+1, gm.Title, err)
				continue
			}
			if err := replaceMeal(ctx, store, hh.ID, meals, gm); err != nil {
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

// buildRepairPrompt asks for cheaper replacements for targets. meals is the
// whole plan: the model only sees the meals it is replacing otherwise, and
// would happily hand back a dish the week already has.
func buildRepairPrompt(hh *db.Household, profile *PreferenceProfile, targets, meals []*db.Meal, budgetCents, totalCents int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The current meal plan costs $%.2f but the budget is $%.2f.\n",
		float64(totalCents)/100, float64(budgetCents)/100)
	b.WriteString("Replace ONLY these meals with cheaper alternatives that honor the same constraints:\n\n")
	replacing := make(map[int64]bool, len(targets))
	for _, m := range targets {
		replacing[m.ID] = true
		fmt.Fprintf(&b, "- %s %s: %q\n", weekdayName(m.Day), m.Slot, m.Title)
	}
	var kept []*db.Meal
	for _, m := range meals {
		if !replacing[m.ID] && !m.IsLeftover && m.Title != "" {
			kept = append(kept, m)
		}
	}
	if len(kept) > 0 {
		b.WriteString("\nThe rest of the week stays as it is. NEVER repeat any of these meals, and never return the same replacement twice - every meal in the week must be a distinct dish:\n\n")
		for _, m := range kept {
			fmt.Fprintf(&b, "- %s %s: %q\n", weekdayName(m.Day), m.Slot, m.Title)
		}
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
func replaceMeal(ctx context.Context, store db.Store, householdID int64, meals []*db.Meal, gm GeneratedMeal) error {
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
	// The slot now holds a different dish: file it in the catalog as
	// persistPlan does and follow that recipe instead of the one the old meal
	// came from (SyncRecipeMeals). Non-fatal, and unlinked if it cannot be
	// filed - following the old recipe would be worse than following none.
	var link *int64
	if recipeID, _, rerr := ensureGeneratedRecipe(ctx, store, householdID, gm); rerr != nil {
		log.Printf("repair: save recipe %q to catalog: %v", gm.Title, rerr)
	} else if recipeID != 0 {
		link = &recipeID
	}
	if err := store.SetMealCatalogRecipe(ctx, target.ID, link); err != nil {
		return fmt.Errorf("link recipe: %w", err)
	}

	// An upsert, not CreateMealRecipe: the meal being replaced already has a
	// meal_recipes row (meal_id is UNIQUE), and a failed insert here used to
	// return before the ingredients were swapped, leaving the new title on
	// the old dish.
	stepsJSON, _ := json.Marshal(gm.Steps)
	if err := store.SetMealSteps(ctx, target.ID, string(stepsJSON), gm.Servings); err != nil {
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
