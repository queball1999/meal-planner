package plan

import (
	"fmt"
	"log"
	"math"

	"goeat/pricing"
)

// perLineUnitCap is the most of one unit a single meal's ingredient line can
// plausibly call for, keyed by canonical unit (pricing.CanonUnit). These are
// deliberately generous - a cap only exists to catch a model that has emitted
// something physically absurd ("681 lb chicken breast", "9 dozen eggs" for a
// weeknight dinner), not to second-guess a large-batch recipe. A unit not
// listed is left alone.
//
// The caps assume a ~4-serving meal; ClampQuantities scales them up for meals
// that cook for more.
var perLineUnitCap = map[string]float64{
	// mass
	"g":  4000,
	"kg": 4,
	"mg": 200000,
	"oz": 140,
	"lb": 9,
	// volume
	"ml":     4000,
	"l":      4,
	"tsp":    120,
	"tbsp":   48,
	"cup":    16,
	"fl-oz":  130,
	"pint":   8,
	"quart":  4,
	"gallon": 1,
	// count / pieces
	"each":    48,
	"clove":   30,
	"slice":   40,
	"can":     10,
	"jar":     6,
	"bottle":  6,
	"package": 6,
	"bag":     6,
	"box":     6,
	"head":    8,
	"bunch":   8,
	"stalk":   24,
	"stick":   8,
	"sprig":   24,
	"loaf":    4,
	"dozen":   4,
}

// ClampQuantities pulls any ingredient quantity that exceeds a sane per-line
// cap (scaled by the meal's serving count) back down to that cap, in place. It
// returns a human-readable note for each line it changed; callers log these so
// an implausible model response is visible rather than silently shopped.
//
// Clamp, not reject: a 21-meal plan is expensive to regenerate, and one bad
// number should not throw the other 20 meals away - the same reasoning as
// Validate tolerating "0 pinch salt".
func ClampQuantities(gp GeneratedPlan) []string {
	var notes []string
	for mi := range gp.Meals {
		m := &gp.Meals[mi]

		servings := m.Servings
		if servings <= 0 {
			servings = 4
		}
		scale := math.Max(1, float64(servings)/4)

		for ii := range m.Ingredients {
			ing := &m.Ingredients[ii]
			maxQty, ok := perLineUnitCap[pricing.CanonUnit(ing.Unit)]
			if !ok {
				continue
			}
			limit := maxQty * scale
			if ing.Quantity > limit {
				note := fmt.Sprintf("%s: %q wants %.4g %s - implausible, clamped to %.4g",
					m.Title, ing.Name, ing.Quantity, ing.Unit, limit)
				notes = append(notes, note)
				log.Printf("plan: clamp quantity: %s", note)
				ing.Quantity = limit
			}
		}
	}
	return notes
}
