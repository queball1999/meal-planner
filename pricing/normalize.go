package pricing

import (
	"strings"
	"unicode"
)

// prepWords are stripped before normalization - they describe preparation, not
// the ingredient itself, so "diced onion" and "onion" should match.
//
// "canned" is not one: a can of diced tomatoes and a fresh tomato are
// different things to buy, and stripping it made them one catalog item, so a
// recipe's fresh tomato was priced (and bought) as a can. catalog.EnsureItem
// still joins "black beans" measured in cans to "canned black beans".
var prepWords = map[string]bool{
	"diced": true, "chopped": true, "sliced": true, "minced": true,
	"crushed": true, "peeled": true, "grated": true, "shredded": true,
	"ground": true, "whole": true, "fresh": true, "dried": true,
	"cooked": true, "raw": true, "frozen": true,
	"organic": true, "boneless": true, "skinless": true, "lean": true,
	"large": true, "medium": true, "small": true, "extra": true,
}

// synonyms maps ingredient aliases to a canonical term so that, e.g.,
// "scallion" and "green onion" resolve to the same cache key.
var synonyms = map[string]string{
	"scallion":       "green onion",
	"spring onion":   "green onion",
	"capsicum":       "bell pepper",
	"zucchini":       "zucchini",
	"courgette":      "zucchini",
	"aubergine":      "eggplant",
	"coriander":      "cilantro",
	"corn starch":    "cornstarch",
	"corn flour":     "cornstarch",
	"chili":          "chilli",
	"chile":          "chilli",
	"stock":          "broth",
	"heavy cream":    "heavy whipping cream",
	"double cream":   "heavy whipping cream",
	"plain flour":    "all-purpose flour",
	"self raising":   "self-rising flour",
	"bicarbonate":    "baking soda",
	"sultana":        "raisin",
	"rocket":         "arugula",
	"swede":          "rutabaga",
	"mangetout":      "snow peas",
	"mange tout":     "snow peas",
	"natural yogurt": "plain yogurt",
}

// Normalize strips prep words, lowercases, singularizes lightly, and applies
// synonym mapping - producing the join key for price_cache, item_product_map,
// and pantry_items (§6.3, §10.2). Deterministic; no LLM.
func Normalize(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))

	// Strip trailing parenthetical qualifiers: "eggs (large)" → "eggs"
	if i := strings.IndexByte(raw, '('); i > 0 {
		raw = strings.TrimSpace(raw[:i])
	}

	// Tokenize, drop prep words.
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || r == ','
	})
	kept := tokens[:0]
	for _, t := range tokens {
		if !prepWords[t] {
			kept = append(kept, t)
		}
	}
	result := strings.Join(kept, " ")

	// Apply synonym map on the full phrase and individual tokens.
	if canon, ok := synonyms[result]; ok {
		result = canon
	}

	// Light singularization: drop trailing 's' only for common food plurals
	// that won't be misread (e.g. "eggs" → "egg", "carrots" → "carrot").
	// Stops at 3-char minimum to avoid mangling short words. "-oes" plurals
	// drop the whole "es" ("tomatoes" → "tomato", not "tomatoe").
	if len(result) > 4 && strings.HasSuffix(result, "oes") {
		result = result[:len(result)-2]
	} else if len(result) > 4 && strings.HasSuffix(result, "s") &&
		!strings.HasSuffix(result, "ss") &&
		!strings.HasSuffix(result, "us") &&
		!strings.HasSuffix(result, "ies") {
		result = result[:len(result)-1]
	}

	return result
}
