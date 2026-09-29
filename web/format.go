package web

import (
	"strconv"
	"strings"

	"goeat/pricing"
)

// countableUnits are "unit" words that add nothing when written next to a
// number: "4 each", "2 count", "3 piece" all read worse than the bare count.
// For these, qtyLabel drops the word and leaves just the number - the
// ingredient or item name beside it already says what is being counted.
var countableUnits = map[string]bool{
	"":      true,
	"each":  true,
	"ea":    true,
	"ct":    true,
	"count": true,
	"pc":    true,
	"pcs":   true,
	"piece": true,
	"unit":  true,
	"whole": true,
	"item":  true,
}

// pluralUnits maps a singular unit to its plural, for the units that actually
// show up in recipes and shopping lines. A unit not listed is left as-is
// (metric/imperial abbreviations - g, kg, ml, tbsp, lb, oz - never pluralize).
var pluralUnits = map[string]string{
	"slice":   "slices",
	"clove":   "cloves",
	"can":     "cans",
	"bunch":   "bunches",
	"head":    "heads",
	"stick":   "sticks",
	"stalk":   "stalks",
	"sprig":   "sprigs",
	"loaf":    "loaves",
	"package": "packages",
	"packet":  "packets",
	"bottle":  "bottles",
	"jar":     "jars",
	"bag":     "bags",
	"box":     "boxes",
	"carton":  "cartons",
	"dozen":   "dozen",
	"sheet":   "sheets",
	"strip":   "strips",
	"fillet":  "fillets",
	"breast":  "breasts",
	"thigh":   "thighs",
	"leg":     "legs",
	"pinch":   "pinches",
	"dash":    "dashes",
	"cup":     "cups",
}

// displayQtyUnit re-expresses a stored (qty, unit) in the household's preferred
// measurement system (pricing.DisplayQuantity). system "" or "as-is" is a
// pass-through. Only builtin weight/volume factors are used, so item-specific
// conversions on exotic units are irrelevant here.
func displayQtyUnit(qty float64, unit, system string) (float64, string) {
	return pricing.DisplayQuantity(qty, unit, system, nil)
}

// displayQtyLabel is displayQtyUnit followed by qtyLabel, for the common case
// of rendering one "1.2 kg" string.
func displayQtyLabel(qty float64, unit, system string) string {
	q, u := displayQtyUnit(qty, unit, system)
	return qtyLabel(q, u)
}

// qtyLabel renders a quantity and unit the way a person would say it. "each"
// and its synonyms collapse to just the number ("4 each" -> "4"); countable
// units pluralize with the quantity ("2 slice" -> "2 slices"); abbreviations
// are left untouched ("1.5 kg" stays "1.5 kg"). A zero or negative quantity
// yields just the unit (or ""), so callers can guard on the empty string.
func qtyLabel(qty float64, unit string) string {
	u := pricing.CanonUnit(unit)
	num := pricing.FormatQty(qty)

	if qty <= 0 {
		if countableUnits[u] {
			return ""
		}
		return u
	}
	if countableUnits[u] {
		return num
	}
	if qty != 1 {
		if p, ok := pluralUnits[u]; ok {
			u = p
		}
	}
	if u == "" {
		return num
	}
	return num + " " + u
}

// fmtQty is the template side of pricing.FormatQty. It also takes a string,
// because catalog recipes keep quantities as text ("2", "1 1/2", "to taste")
// and older generated ones were saved at full float precision
// ("907.1847412751217"): a plain number is rounded, anything else is shown as
// written.
func fmtQty(v any) string {
	switch q := v.(type) {
	case float64:
		return pricing.FormatQty(q)
	case int:
		return strconv.Itoa(q)
	case int64:
		return strconv.FormatInt(q, 10)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(q), 64); err == nil {
			return pricing.FormatQty(f)
		}
		return q
	}
	return ""
}
