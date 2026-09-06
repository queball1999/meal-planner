package plan

import (
	"fmt"
	"strings"
	"time"

	"goeat/db"
)

const systemPrompt = `You are a household meal planner. Generate a weekly meal plan as structured JSON.

The plan covers breakfast, lunch, and dinner for 7 days (sunday through saturday).
Return ONLY valid JSON matching this schema - no prose, no markdown fences, no explanation:

{
  "meals": [
    {
      "day": "monday",
      "slot": "breakfast",
      "title": "Oatmeal with berries",
      "effort": "quick",
      "servings": 2,
      "cooked_portions": 2,
      "ingredients": [
        {"name": "rolled oats", "quantity": 1.0, "unit": "cup"},
        {"name": "blueberries", "quantity": 0.5, "unit": "cup"}
      ],
      "steps": ["Boil 2 cups water.", "Stir in oats and cook 5 min.", "Top with berries."]
    }
  ]
}

Rules:
- day values: sunday, monday, tuesday, wednesday, thursday, friday, saturday
- slot values: breakfast, lunch, dinner
- effort values: quick, standard, elaborate
- Generate exactly 21 meals - one per slot per day, all 7 days covered
- Ingredients use US/imperial units (cup, oz, lb, tbsp, tsp, piece, clove, etc.)
- Steps are numbered imperatives, 3-8 per meal
- Plan slightly under the budget to leave headroom; the pricing engine will cost the actual total`

// storeNames returns the names of the household's enabled stores, plus
// whether any of them is a warehouse club.
func storeNames(stores []*db.GroceryStore) (names []string, hasWarehouse bool) {
	for _, st := range stores {
		if !st.Enabled {
			continue
		}
		names = append(names, st.Name)
		if st.Kind == "warehouse" {
			hasWarehouse = true
		}
	}
	return names, hasWarehouse
}

// BuildPrompt assembles the system and user prompts for plan generation (§7.3, §7.4).
func BuildPrompt(hh *db.Household, profile *PreferenceProfile, stores []*db.GroceryStore, weekStart, weekEnd time.Time) (system, user string) {
	var b strings.Builder

	fmt.Fprintf(&b, "Household size: %d people\n", hh.HouseholdSize)
	fmt.Fprintf(&b, "Weekly budget: $%.0f\n", float64(hh.WeeklyBudgetCents)/100)
	fmt.Fprintf(&b, "Week: %s through %s\n", weekStart.Format("2006-01-02 (Monday)"), weekEnd.Format("2006-01-02 (Monday)"))

	// Selected stores (§10.1 GroceryStore): steers ingredient choices toward
	// what the household can actually buy, rather than assuming a generic
	// well-stocked supermarket. Pricing still resolves per-store separately -
	// this is about plausibility of the ingredient list, not price lookup.
	if names, warehouseOnly := storeNames(stores); len(names) > 0 {
		fmt.Fprintf(&b, "\nGrocery stores this household shops at: %s\n", strings.Join(names, ", "))
		b.WriteString("Favor ordinary ingredients widely stocked at these stores; avoid specialty or hard-to-find items unless one of the stores is a specialty grocer.\n")
		if warehouseOnly {
			b.WriteString("A warehouse club is available - bulk pack sizes are fine to lean into for pantry staples, but do not require a warehouse trip for every meal.\n")
		}
	}

	// Hard constraints
	if len(profile.Allergies) > 0 {
		fmt.Fprintf(&b, "\nAllergens - NEVER include these in any ingredient:\n- %s\n",
			strings.Join(profile.Allergies, ", "))
	}
	if len(profile.DietTags) > 0 {
		fmt.Fprintf(&b, "\nDiet (hard constraints - respect strictly):\n- %s\n",
			strings.Join(profile.DietTags, ", "))
	}

	// Soft preferences
	if len(profile.Cuisines) > 0 {
		fmt.Fprintf(&b, "\nPreferred cuisines (lean toward these):\n- %s\n",
			strings.Join(profile.Cuisines, ", "))
	}
	if len(profile.Dislikes) > 0 {
		fmt.Fprintf(&b, "\nFoods to avoid if possible (soft):\n- %s\n",
			strings.Join(profile.Dislikes, ", "))
	}
	if profile.LeftoverTolerance {
		b.WriteString("\nLeftover tolerance: ON - you may plan batch-cook meals that cover a later slot as leftovers. Set cooked_portions higher than servings and note in the title when a meal is intentional leftovers.\n")
	}

	// Per-slot hints
	writeSlotHint := func(slot, effort, hints string) {
		fmt.Fprintf(&b, "\n%s - effort: %s", strings.Title(slot), effort) //nolint:staticcheck
		if hints != "" {
			fmt.Fprintf(&b, "\n  Typical foods: %s", hints)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nMeal slot preferences:\n")
	writeSlotHint("breakfast", profile.EffortBreakfast, profile.HintsBreakfast)
	writeSlotHint("lunch", profile.EffortLunch, profile.HintsLunch)
	writeSlotHint("dinner", profile.EffortDinner, profile.HintsDinner)

	// Feedback digest
	if len(profile.FeedbackLiked) > 0 {
		fmt.Fprintf(&b, "\nPreviously liked (include similar):\n- %s\n",
			strings.Join(profile.FeedbackLiked, ", "))
	}
	if len(profile.FeedbackDisliked) > 0 {
		fmt.Fprintf(&b, "\nPreviously disliked (avoid these):\n- %s\n",
			strings.Join(profile.FeedbackDisliked, ", "))
	}

	b.WriteString("\nReturn the JSON plan now.")
	return systemPrompt, b.String()
}
