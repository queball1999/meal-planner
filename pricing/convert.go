package pricing

import (
	"strings"

	"goeat/db"
)

// unitAliases folds common spellings and plurals onto one canonical token so
// "Tablespoons", "tbsp", and "tbs" all convert the same way.
var unitAliases = map[string]string{
	"":        "each",
	"ea":      "each", "each": "each", "ct": "each", "count": "each",
	"piece": "each", "pieces": "each", "pc": "each", "pcs": "each", "unit": "each",
	"lb": "lb", "lbs": "lb", "pound": "lb", "pounds": "lb", "#": "lb",
	"oz": "oz", "ounce": "oz", "ounces": "oz",
	"g": "g", "gram": "g", "grams": "g", "gm": "g",
	"kg": "kg", "kilogram": "kg", "kilograms": "kg", "kilo": "kg",
	"mg": "mg", "milligram": "mg", "milligrams": "mg",
	"ml": "ml", "milliliter": "ml", "milliliters": "ml", "millilitre": "ml", "millilitres": "ml", "cc": "ml",
	"l": "l", "liter": "l", "liters": "l", "litre": "l", "litres": "l",
	"tsp": "tsp", "teaspoon": "tsp", "teaspoons": "tsp",
	"tbsp": "tbsp", "tbs": "tbsp", "tablespoon": "tbsp", "tablespoons": "tbsp",
	"cup": "cup", "cups": "cup",
	"fl oz": "fl-oz", "fl-oz": "fl-oz", "floz": "fl-oz", "fluid ounce": "fl-oz", "fluid ounces": "fl-oz",
	"pt": "pint", "pint": "pint", "pints": "pint",
	"qt": "quart", "quart": "quart", "quarts": "quart",
	"gal": "gallon", "gallon": "gallon", "gallons": "gallon",
	"clove": "clove", "cloves": "clove",
	"can": "can", "cans": "can", "tin": "can", "tins": "can",
	"package": "package", "packages": "package", "pkg": "package", "pack": "package", "packs": "package",
	"bunch": "bunch", "bunches": "bunch",
	"head": "head", "heads": "head",
	"slice": "slice", "slices": "slice",
	"stick": "stick", "sticks": "stick",
	"stalk": "stalk", "stalks": "stalk",
	"sprig": "sprig", "sprigs": "sprig",
	"pinch": "pinch", "pinches": "pinch",
	"dash": "dash", "dashes": "dash",
	"bottle": "bottle", "bottles": "bottle",
	"jar": "jar", "jars": "jar",
	"bag": "bag", "bags": "bag",
	"box": "box", "boxes": "box",
	"dozen": "dozen", "doz": "dozen",
}

// CanonUnit returns the canonical token for a free-text unit string. Unknown
// units pass through lower-cased and trimmed so item-specific conversions on
// exotic units still line up.
func CanonUnit(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimSuffix(u, ".")
	if c, ok := unitAliases[u]; ok {
		return c
	}
	return u
}

// builtinConversion is one directed edge: 1 From = Factor To. The reverse edge
// is added automatically.
type builtinConversion struct {
	From, To string
	Factor   float64
}

// builtinConversions are the fixed metric/imperial factors. Item-specific and
// DB-seeded edges are layered on top of these in Convert.
var builtinConversions = []builtinConversion{
	{"g", "kg", 0.001},
	{"kg", "lb", 2.2046226218},
	{"g", "oz", 0.0352739619},
	{"lb", "oz", 16},
	{"lb", "g", 453.59237},
	{"g", "mg", 1000},
	{"ml", "l", 0.001},
	{"l", "cup", 4.2267528377},
	{"ml", "tsp", 0.2028841362},
	{"tbsp", "tsp", 3},
	{"cup", "tbsp", 16},
	{"fl-oz", "tbsp", 2},
	{"cup", "fl-oz", 8},
	{"pint", "cup", 2},
	{"quart", "pint", 2},
	{"gallon", "quart", 4},
	{"dozen", "each", 12},
}

type convEdge struct {
	to     string
	factor float64
}

// Convert expresses qty (given in from) in the to unit, using the builtin
// factors plus any per-item / global edges in extra. It BFS-walks the
// bidirectional conversion graph; ok is false when no path connects the units.
func Convert(qty float64, from, to string, extra []*db.UnitConversion) (float64, bool) {
	f, t := CanonUnit(from), CanonUnit(to)
	if f == t {
		return qty, true
	}

	adj := map[string][]convEdge{}
	add := func(a, b string, factor float64) {
		if factor == 0 {
			return
		}
		a, b = CanonUnit(a), CanonUnit(b)
		adj[a] = append(adj[a], convEdge{b, factor})
		adj[b] = append(adj[b], convEdge{a, 1 / factor})
	}
	for _, e := range builtinConversions {
		add(e.From, e.To, e.Factor)
	}
	for _, c := range extra {
		if c != nil {
			add(c.FromUnit, c.ToUnit, c.Factor)
		}
	}

	type node struct {
		unit string
		mult float64
	}
	seen := map[string]bool{f: true}
	queue := []node{{f, 1}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n.unit == t {
			return qty * n.mult, true
		}
		for _, e := range adj[n.unit] {
			if !seen[e.to] {
				seen[e.to] = true
				queue = append(queue, node{e.to, n.mult * e.factor})
			}
		}
	}
	return 0, false
}
