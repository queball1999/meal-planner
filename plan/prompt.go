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
        {"name": "rolled oats", "quantity": 1.0, "unit": "cup", "est_price_cents": 60, "item_unit": "g", "conversions": [{"from": "cup", "to": "g", "factor": 90}]},
        {"name": "blueberries", "quantity": 0.5, "unit": "cup", "est_price_cents": 150, "item_unit": "g", "conversions": [{"from": "cup", "to": "g", "factor": 150}]},
        {"name": "eggs", "quantity": 2, "unit": "each", "est_price_cents": 50, "item_unit": "each"},
        {"name": "canned black beans", "quantity": 1, "unit": "can", "est_price_cents": 129, "item_unit": "can"}
      ],
      "steps": ["Boil 2 cups water.", "Stir in oats and cook 5 min.", "Top with berries."],
      "prep_minutes": 5,
      "cook_minutes": 5,
      "tags": ["breakfast", "vegetarian", "quick"]
    }
  ]
}

Rules:
- day values: sunday, monday, tuesday, wednesday, thursday, friday, saturday
- slot values: breakfast, lunch, dinner
- effort values: quick, standard, elaborate
- Generate exactly 21 meals - one per slot per day, all 7 days covered
- "servings" is the recipe's yield and MUST equal the household size stated below,
  for every meal. Scale the ingredient quantities to match that yield - a 4-person
  plan lists roughly twice the quantities of a 2-person one. The app rescales a
  day's recipes from these numbers whenever the headcount for that day changes, so
  an inconsistent servings/quantity pair skews every later adjustment.
- "cooked_portions" is how many portions the recipe actually produces: equal to
  "servings" for a normal meal, higher when you deliberately batch-cook for
  leftovers. Never 0, and never below "servings".
- Every ingredient MUST have a non-empty "unit" - never omit it or leave it blank.
  Use whatever real, purchasable unit fits how the ingredient is actually bought/measured:
  weight/volume (oz, lb, g, cup, tbsp, tsp) for bulk foods, or count units (each, can,
  jar, package, bag, bunch, head, clove, slice) for items sold as discrete pieces.
  A canned good is quantity in "can", never a bare number with no unit.
- Every ingredient MUST have "est_price_cents": your own best-guess typical US grocery
  price, as integer cents, for exactly the stated quantity (e.g. one 15 oz can of black
  beans ≈ 129). This is a fallback the app keeps and uses only when no live price is
  found later - it does not need to be precise, but it must be a real positive number,
  never 0 or omitted.
- "item_unit" is the unit this ingredient is actually stocked and bought in, which is
  often NOT how the recipe measures it: flour is bought by weight ("g") even when a
  recipe calls for cups; eggs and lemons are bought "each"; a canned good is bought by
  the "can". Give the plain purchasable stock unit here for every ingredient.
- "conversions" is REQUIRED whenever "unit" differs from "item_unit" and the two are
  not a standard metric/imperial pair the app already knows (g<->kg, lb<->oz, tsp<->tbsp,
  cup<->ml, dozen<->each). Each entry is {"from","to","factor"} meaning 1 from = factor
  to, and at least one entry MUST connect "unit" to "item_unit". Examples: cup of flour
  -> {"from":"cup","to":"g","factor":120}; clove of garlic -> {"from":"clove","to":"g","factor":5};
  a recipe "egg" when item_unit is "g" -> {"from":"each","to":"g","factor":50}. Omit
  "conversions" only when "unit" and "item_unit" are the same or a known pair.
- Steps are numbered imperatives, 3-8 per meal
- "prep_minutes" and "cook_minutes" are your best estimate of hands-on and
  cooking time. Every generated meal is saved to the household's recipe
  catalog to cook again later, and a recipe with no times cannot be found by
  "what can I make in 20 minutes".
- "tags" are 2-5 short lowercase labels for finding this recipe later: the meal
  type, the cuisine, the main protein, and any diet it satisfies. Not
  sentences, not ingredient lists.
- Ingredient "name" must be the plain grocery name of the thing, as it would be
  written on a shopping list: "chicken breast", not "boneless skinless organic
  chicken breast, cubed". Put preparation in the steps, not the name. The app
  matches these names against the household's grocery catalog to track prices
  and pantry stock, and a name loaded with adjectives matches nothing.
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

	// Servings come from what the household eats, not from how many people it
	// contains: two adults and two toddlers is three servings' worth of food,
	// and asking for four would over-buy every week.
	servings := profile.TotalPortions(hh.HouseholdSize)
	if len(profile.Members) > 0 {
		fmt.Fprintf(&b, "Household: %d people, eating %d standard servings between them - every meal must have servings = %d\n",
			len(profile.Members), servings, servings)
		b.WriteString("Who eats here:\n")
		for _, m := range profile.Members {
			fmt.Fprintf(&b, "- %s: eats %.2g of a standard adult serving", m.Name, m.PortionFactor)
			if m.Notes != "" {
				fmt.Fprintf(&b, " (%s)", m.Notes)
			}
			b.WriteString("\n")
		}
		b.WriteString("Respect any note above as a constraint on that person's meals; where a note conflicts with a household preference, the note wins for that person only.\n")
	} else {
		fmt.Fprintf(&b, "Household size: %d people - every meal must have servings = %d\n", servings, servings)
	}
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
		b.WriteString("\nLeftover tolerance: ON - you may plan batch-cook meals that cover a later slot as leftovers. Set cooked_portions higher than servings and note in the title when a meal is intentional leftovers. The week's first day (Sunday) must be a fresh, non-leftover meal at every slot - nothing earlier in the week exists yet to batch-cook from.\n")
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
