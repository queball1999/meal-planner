package plan

import (
	"math"

	"goeat/db"
)

// GeneratedIngredient is one ingredient line in the LLM's JSON output (§7.4).
type GeneratedIngredient struct {
	Name          string  `json:"name"`
	Quantity      float64 `json:"quantity"`
	Unit          string  `json:"unit"`
	EstPriceCents int64   `json:"est_price_cents"` // the LLM's own rough US grocery price for this quantity - kept as a last-resort pricing fallback (§6.4)

	// ItemUnit is the unit this ingredient's catalog item should be stocked and
	// bought in - grams for flour, "each" for eggs - which is often not the
	// unit the recipe measures in (a clove, a cup). Used only when the item is
	// created for the first time; an existing item keeps whatever unit it has.
	ItemUnit string `json:"item_unit"`

	// Conversions are item-specific unit factors the builtin metric/imperial
	// graph cannot know: "1 clove = 5 g", "1 egg = 50 g". Written as catalog
	// conversion edges so the shopping list can reconcile this line's unit
	// against the item's stock unit instead of guessing. Optional.
	Conversions []GeneratedConversion `json:"conversions"`
}

// GeneratedConversion is one unit factor the model supplies for an ingredient:
// 1 From = Factor To.
type GeneratedConversion struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Factor float64 `json:"factor"`
}

// GeneratedMeal is one meal in the LLM's JSON output (§7.4).
type GeneratedMeal struct {
	Day            string                `json:"day"`  // "sunday"…"saturday"
	Slot           string                `json:"slot"` // "breakfast"|"lunch"|"dinner"
	Title          string                `json:"title"`
	Effort         string                `json:"effort"` // "quick"|"standard"|"elaborate"
	Servings       int                   `json:"servings"`
	CookedPortions int                   `json:"cooked_portions"`
	Ingredients    []GeneratedIngredient `json:"ingredients"`
	Steps          []string              `json:"steps"`

	// Asked for so the meal can be saved to the recipe catalog as something
	// worth cooking again, rather than a title and a step list with no times
	// and no way to find it. Optional in the response - an older model that
	// ignores them still produces a valid plan.
	PrepMinutes int      `json:"prep_minutes"`
	CookMinutes int      `json:"cook_minutes"`
	Tags        []string `json:"tags"`
}

// GeneratedPlan is the top-level LLM JSON response (§7.4).
type GeneratedPlan struct {
	Meals []GeneratedMeal `json:"meals"`
}

// PreferenceProfile is the resolved input to the generation prompt (§4.6).
type PreferenceProfile struct {
	Allergies         []string // hard excludes (§4.2)
	DietTags          []string // "vegetarian"|"vegan"|"pescatarian"|"keto"|"low-carb"|"gluten-free"
	Cuisines          []string // soft preferences
	Dislikes          []string // soft avoids
	LeftoverTolerance bool
	HintsBreakfast    string // raw free-text
	HintsLunch        string
	HintsDinner       string
	EffortBreakfast   string
	EffortLunch       string
	EffortDinner      string
	FeedbackLiked     []string // recent liked meal titles
	FeedbackDisliked  []string // recent disliked meal titles

	// Members is who the household actually cooks for. Servings come from the
	// sum of their portion factors, not from a headcount, so two adults and
	// two toddlers is 3 servings rather than 4. Empty for a household that has
	// not set members up, in which case household size stands in.
	Members []*db.HouseholdMember
}

// TotalPortions is what every meal in a generated plan should serve: the sum
// of the members' portion factors, or the plain household size when no members
// have been set up. Rounded to the nearest whole serving because that is what
// a recipe card can state.
func (p *PreferenceProfile) TotalPortions(householdSize int) int {
	if len(p.Members) == 0 {
		if householdSize < 1 {
			return 1
		}
		return householdSize
	}
	var total float64
	for _, m := range p.Members {
		total += m.PortionFactor
	}
	n := int(math.Round(total))
	if n < 1 {
		n = 1
	}
	return n
}
