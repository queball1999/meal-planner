package pricing

import "goeat/db"

// Measurement systems for shopping-list display. A household picks one on the
// Preferences page; "as-is" is the default and converts nothing.
const (
	SystemAsIs     = "as-is"
	SystemMetric   = "metric"
	SystemImperial = "imperial"
)

// minReadable is the smallest value a promoted unit is allowed to show. Walking
// the ladder from the largest unit down, the first unit whose value is at least
// this is chosen - so 1200 g becomes "1.2 kg" but 900 g stays "900 g", and
// 20 oz becomes "1.25 lb" but 12 oz stays "12 oz". A flat "over 100" cutoff was
// considered and rejected: it renders 100 g as "0.1 kg".
const minReadable = 1.0

// displayLadders list a system's units for one dimension, smallest first. The
// smallest is used as the common base to convert into before choosing.
var displayLadders = map[string][]string{
	SystemMetric + "/mass":     {"g", "kg"},
	SystemImperial + "/mass":   {"oz", "lb"},
	SystemMetric + "/volume":   {"ml", "l"},
	SystemImperial + "/volume": {"fl-oz", "cup", "pint", "quart", "gallon"},
}

// dimensionOf classifies a canonical unit as "mass" or "volume". Countable and
// unknown units return "" - they are never rescaled.
func dimensionOf(u string) string {
	switch u {
	case "mg", "g", "kg", "oz", "lb":
		return "mass"
	case "ml", "l", "tsp", "tbsp", "fl-oz", "cup", "pint", "quart", "gallon":
		return "volume"
	}
	return ""
}

// DisplayQuantity re-expresses (qty, unit) in the household's preferred
// measurement system, promoting to the next unit up so the number reads
// naturally (see minReadable). It returns the input unchanged when:
//   - system is "" or "as-is",
//   - qty is not positive,
//   - the unit is countable/unknown (each, can, bunch, ...),
//   - no conversion path exists.
//
// extra carries item-specific conversion edges; pass nil to use only the
// builtin metric/imperial factors, which already cover every ladder unit.
func DisplayQuantity(qty float64, unit, system string, extra []*db.UnitConversion) (float64, string) {
	if system == "" || system == SystemAsIs || qty <= 0 {
		return qty, unit
	}
	cu := CanonUnit(unit)
	ladder := displayLadders[system+"/"+dimensionOf(cu)]
	if len(ladder) == 0 {
		return qty, unit
	}

	base := ladder[0]
	baseVal, ok := Convert(qty, cu, base, extra)
	if !ok {
		return qty, unit
	}

	// Largest unit down: take the first whose value is readable. If even the
	// smallest is below the floor (e.g. 300 ml as metric mass never happens,
	// but 0.3 kg would), fall back to the smallest.
	for i := len(ladder) - 1; i >= 0; i-- {
		v, ok := Convert(baseVal, base, ladder[i], extra)
		if !ok {
			continue
		}
		if v >= minReadable || i == 0 {
			return v, ladder[i]
		}
	}
	return baseVal, base
}
