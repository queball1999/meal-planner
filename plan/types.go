package plan

// GeneratedIngredient is one ingredient line in the LLM's JSON output (§7.4).
type GeneratedIngredient struct {
	Name          string  `json:"name"`
	Quantity      float64 `json:"quantity"`
	Unit          string  `json:"unit"`
	EstPriceCents int64   `json:"est_price_cents"` // the LLM's own rough US grocery price for this quantity - kept as a last-resort pricing fallback (§6.4)
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
}
