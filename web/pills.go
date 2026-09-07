package web

import (
	"fmt"
	"strings"
)

// One 8-hue pill palette, shared by every column whose values are scanned by
// color rather than read: the item catalog's source/unit/category, the
// shopping list's meal tags, and anything added later. Sharing it is the
// point - two columns that both show "dairy" or both show "Chili" agree on
// what color that is, and a reader who has learned one column's colors has
// learned them all.
//
// The tokens are declared in tokens.css (light and dark) and the classes in
// components.css.
const pillHues = 8

// hueIndex hashes a value to 1..pillHues. Deterministic and stable across
// processes (no map iteration, no rand), so a value keeps its color between
// page loads and between users.
func hueIndex(s string) int {
	var h uint32
	for i := 0; i < len(s); i++ {
		h = h*31 + uint32(s[i])
	}
	return int(h%pillHues) + 1
}

// hueClass is hueIndex rendered as the CSS modifier.
func hueClass(s string) string {
	return fmt.Sprintf("badge-hue-%d", hueIndex(s))
}

// pillSourceHues pins the handful of values whose color carries meaning
// rather than just identity. A provenance column reads better when
// "manual" is always the warm one and "ai" always the purple one, whatever
// the hash would have picked - and pinning them also keeps two sources from
// colliding on the same hue, which is the one case where the hash's
// indifference is actually wrong.
//
// Anything not listed falls through to the hash, so a new source added later
// still gets a color without a code change.
var pillSourceHues = map[string]int{
	"builtin":  6, // indigo
	"seed":     6,
	"manual":   4, // amber
	"operator": 4,
	"ai":       2, // purple
	"llm":      2,
	"estimate": 7, // brown
	"scraped":  1, // blue
	"scrape":   1,
	"imported": 3, // teal
	"cached":   8, // cyan
}

// pillClass returns the badge modifier for one value in a color-scanned
// column. `kind` selects the pinned-color table ("source" today); every other
// kind - units, categories - is pure hash.
//
// An empty value is muted rather than hashed: "" is the absence of a value,
// and giving it a confident color makes an empty cell look like data.
func pillClass(kind, value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return "badge-muted"
	}
	if kind == "source" {
		if n, ok := pillSourceHues[v]; ok {
			return fmt.Sprintf("badge-hue-%d", n)
		}
	}
	return hueClass(v)
}
