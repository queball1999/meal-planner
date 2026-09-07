package plan

import (
	"fmt"
	"strings"
)

var validDays = map[string]bool{
	"sunday": true, "monday": true, "tuesday": true, "wednesday": true,
	"thursday": true, "friday": true, "saturday": true,
}

var validSlots = map[string]bool{
	"breakfast": true, "lunch": true, "dinner": true,
}

var validEfforts = map[string]bool{
	"quick": true, "standard": true, "elaborate": true,
}

// Validate checks the generated plan for schema validity, allergy violations,
// and diet tag compliance (§7.5). All 21 day×slot combinations must be present.
func Validate(gp GeneratedPlan, profile *PreferenceProfile) error {
	if len(gp.Meals) != 21 {
		return fmt.Errorf("plan has %d meals, want 21", len(gp.Meals))
	}

	// Build lowercase allergy and disliked-food sets for ingredient scanning.
	allergySet := toLowerSet(profile.Allergies)
	dietRules := dietIngredientRules(profile.DietTags)

	seen := make(map[string]bool, 21) // "day|slot" → true
	for i, m := range gp.Meals {
		day := strings.ToLower(m.Day)
		slot := strings.ToLower(m.Slot)
		effort := strings.ToLower(m.Effort)

		if !validDays[day] {
			return fmt.Errorf("meal %d: invalid day %q", i, m.Day)
		}
		if !validSlots[slot] {
			return fmt.Errorf("meal %d: invalid slot %q", i, m.Slot)
		}
		if !validEfforts[effort] {
			return fmt.Errorf("meal %d: invalid effort %q", i, m.Effort)
		}
		if m.Title == "" {
			return fmt.Errorf("meal %d (%s %s): missing title", i, day, slot)
		}
		if m.Servings <= 0 {
			return fmt.Errorf("meal %d (%s): servings must be > 0", i, m.Title)
		}
		// Default through the slice, not the loop copy: m is a value copy, so
		// the old assignment was discarded and meals kept landing in the DB
		// with cooked_portions 0 - which then gave portion scaling nothing to
		// scale from.
		if m.CookedPortions <= 0 {
			gp.Meals[i].CookedPortions = m.Servings
		}
		if len(m.Ingredients) == 0 {
			return fmt.Errorf("meal %d (%s): no ingredients", i, m.Title)
		}
		if len(m.Steps) == 0 {
			return fmt.Errorf("meal %d (%s): no steps", i, m.Title)
		}

		key := day + "|" + slot
		if seen[key] {
			return fmt.Errorf("duplicate meal for %s %s", day, slot)
		}
		seen[key] = true

		// Allergy check - hard constraint.
		for _, ing := range m.Ingredients {
			if strings.TrimSpace(ing.Unit) == "" {
				return fmt.Errorf("meal %q: ingredient %q is missing a unit", m.Title, ing.Name)
			}
			// Negative is a real error, but 0 is not: "salt to taste", "garnish",
			// "cooking spray" and the like are routinely expressed as quantity 0
			// by the LLM, and always have been - rejecting the whole 21-meal
			// plan over one seasoning line is a much worse failure mode than
			// the cosmetic "0 pinch salt" it would otherwise show.
			if ing.Quantity < 0 {
				return fmt.Errorf("meal %q: ingredient %q has a negative quantity", m.Title, ing.Name)
			}
			nameLower := strings.ToLower(ing.Name)
			for allergen := range allergySet {
				if strings.Contains(nameLower, allergen) {
					return fmt.Errorf("meal %q contains allergen %q in ingredient %q",
						m.Title, allergen, ing.Name)
				}
			}
			// Diet tag checks.
			for _, rule := range dietRules {
				if rule(nameLower) {
					return fmt.Errorf("meal %q violates diet constraint: ingredient %q",
						m.Title, ing.Name)
				}
			}
		}
	}

	// Ensure all 21 combinations are present.
	for day := range validDays {
		for slot := range validSlots {
			if !seen[day+"|"+slot] {
				return fmt.Errorf("missing meal for %s %s", day, slot)
			}
		}
	}

	return nil
}

func toLowerSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, v := range items {
		s[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return s
}

// dietIngredientRules returns ingredient-name predicate functions for each diet tag.
// Each returns true if the ingredient violates that diet.
func dietIngredientRules(dietTags []string) []func(string) bool {
	tagSet := toLowerSet(dietTags)
	var rules []func(string) bool

	if tagSet["vegetarian"] || tagSet["vegan"] {
		meat := []string{"beef", "pork", "chicken", "turkey", "lamb", "veal",
			"duck", "bacon", "ham", "sausage", "pepperoni", "salami",
			"chorizo", "anchovies", "lard", "gelatin", "tallow"}
		rules = append(rules, containsAny(meat))
	}
	if tagSet["vegan"] {
		animal := []string{"milk", "cream", "butter", "cheese", "yogurt",
			"egg", "honey", "whey", "casein", "lactose"}
		rules = append(rules, containsAny(animal))
	}
	if tagSet["pescatarian"] {
		landMeat := []string{"beef", "pork", "chicken", "turkey", "lamb",
			"duck", "bacon", "ham", "sausage", "pepperoni",
			"salami", "chorizo", "lard", "tallow"}
		rules = append(rules, containsAny(landMeat))
	}
	if tagSet["gluten-free"] {
		gluten := []string{"wheat", "flour", "bread", "pasta", "barley",
			"rye", "semolina", "spelt", "farro", "bulgur",
			"couscous", "malt", "triticale"}
		rules = append(rules, containsAny(gluten))
	}
	if tagSet["keto"] || tagSet["low-carb"] {
		highCarb := []string{"sugar", "honey", "syrup", "rice", "pasta",
			"bread", "potato", "flour", "oat", "corn", "bean",
			"lentil", "chickpea"}
		rules = append(rules, containsAny(highCarb))
	}

	return rules
}

func containsAny(terms []string) func(string) bool {
	return func(ing string) bool {
		for _, t := range terms {
			if strings.Contains(ing, t) {
				return true
			}
		}
		return false
	}
}
