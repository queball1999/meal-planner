package web

import (
	"strings"
	"testing"
)

func TestPillClassStableAndInRange(t *testing.T) {
	values := []string{"dairy", "produce", "lb", "oz", "each", "canned goods", "Baking"}
	for _, v := range values {
		got := pillClass("category", v)
		if !strings.HasPrefix(got, "badge-hue-") {
			t.Fatalf("pillClass(category, %q) = %q, want badge-hue-N", v, got)
		}
		if got != pillClass("category", v) {
			t.Fatalf("pillClass(category, %q) is not deterministic", v)
		}
		// The same value in a different column has to land on the same color,
		// which is the whole reason there is one palette rather than three.
		if u := pillClass("unit", v); u != got {
			t.Fatalf("pillClass disagrees across kinds for %q: category=%q unit=%q", v, got, u)
		}
	}
}

func TestPillClassIgnoresCaseAndSpace(t *testing.T) {
	if a, b := pillClass("category", "Dairy"), pillClass("category", "  dairy "); a != b {
		t.Fatalf("case/space changed the color: %q vs %q", a, b)
	}
}

func TestPillClassEmptyIsMuted(t *testing.T) {
	for _, v := range []string{"", "   "} {
		if got := pillClass("unit", v); got != "badge-muted" {
			t.Fatalf("pillClass(unit, %q) = %q, want badge-muted", v, got)
		}
	}
}

// A pinned source keeps its color whatever the hash would have said, and two
// pinned sources must not share one - a provenance column where "manual" and
// "ai" are the same color is worse than no color at all.
func TestPillSourcePinnedColorsAreDistinct(t *testing.T) {
	if got := pillClass("source", "manual"); got != "badge-hue-4" {
		t.Fatalf("pillClass(source, manual) = %q, want badge-hue-4", got)
	}
	seen := map[string][]string{}
	for name := range pillSourceHues {
		c := pillClass("source", name)
		seen[c] = append(seen[c], name)
	}
	// Aliases for the same concept (manual/operator, ai/llm) are meant to
	// share; genuinely different sources are not.
	aliases := map[string]string{
		"seed": "builtin", "operator": "manual", "llm": "ai", "scrape": "scraped",
	}
	for class, names := range seen {
		if len(names) < 2 {
			continue
		}
		root := ""
		for _, n := range names {
			r := n
			if a, ok := aliases[n]; ok {
				r = a
			}
			if root == "" {
				root = r
			} else if r != root {
				t.Fatalf("distinct sources %v collide on %s", names, class)
			}
		}
	}
}

// The shopping list's meal tags moved onto the shared palette; a title must
// still get one stable color.
func TestMealColorClassUsesSharedPalette(t *testing.T) {
	got := mealColorClass("Chicken Quesadillas")
	if !strings.HasPrefix(got, "badge-hue-") {
		t.Fatalf("mealColorClass = %q, want badge-hue-N", got)
	}
	if got != mealColorClass("Chicken Quesadillas") {
		t.Fatal("mealColorClass is not deterministic")
	}
}
