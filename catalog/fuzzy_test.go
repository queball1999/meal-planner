package catalog

import (
	"testing"

	"goeat/db"
)

// The case the whole feature exists for: a plural, and a descriptive name
// wrapping a catalog name, both have to read as the same food.
func TestScoreMatchesRealWorldVariants(t *testing.T) {
	pairs := []struct {
		a, b string
		min  float64
	}{
		{"chicken breast", "chicken breasts", 1.0},
		{"boneless skinless chicken breasts", "chicken breast", 1.0},
		{"fresh organic tomatoes", "tomato", 1.0},
		{"large yellow onion", "yellow onion", 1.0},
		{"extra virgin olive oil", "olive oil", 1.0},
		// Partial overlap: plausible, not certain.
		{"red bell pepper", "bell pepper", SuggestScore},
		{"sharp cheddar cheese", "cheddar cheese", SuggestScore},
	}
	for _, p := range pairs {
		if got := Score(p.a, p.b); got < p.min {
			t.Errorf("Score(%q, %q) = %.3f, want >= %.3f", p.a, p.b, got, p.min)
		}
	}
}

// Different foods must not be linked. A wrong automatic merge moves prices,
// pantry stock and history, and is close to invisible afterwards.
func TestScoreRejectsDifferentFoods(t *testing.T) {
	pairs := [][2]string{
		{"chicken breast", "beef chuck roast"},
		{"olive oil", "canola oil"}, // shares "oil" only
		{"whole milk", "almond butter"},
		{"white rice", "black beans"},
	}
	for _, p := range pairs {
		if got := Score(p[0], p[1]); got >= AutoLinkScore {
			t.Errorf("Score(%q, %q) = %.3f, would auto-link two different foods", p[0], p[1], got)
		}
	}
}

func TestScoreEmpty(t *testing.T) {
	for _, p := range [][2]string{{"", "milk"}, {"milk", ""}, {"", ""}} {
		if got := Score(p[0], p[1]); got != 0 {
			t.Errorf("Score(%q, %q) = %v, want 0", p[0], p[1], got)
		}
	}
}

func TestSingular(t *testing.T) {
	cases := map[string]string{
		"breasts":  "breast",
		"tomatoes": "tomato",
		"berries":  "berry",
		"onion":    "onion",
		"grass":    "grass", // -ss is not a plural
		"is":       "is",    // too short to strip
	}
	for in, want := range cases {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestItemsRanksAndFilters(t *testing.T) {
	items := []*db.Item{
		{ID: 1, Name: "Chicken breast", NormalizedTerm: "chicken breast", Source: "builtin"},
		{ID: 2, Name: "Chicken thigh", NormalizedTerm: "chicken thigh", Source: "builtin"},
		{ID: 3, Name: "Olive oil", NormalizedTerm: "olive oil", Source: "manual"},
		// An auto placeholder is itself an unmatched name, so offering it as a
		// match would chain two guesses together.
		{ID: 4, Name: "chicken breasts", NormalizedTerm: "chicken breasts", Source: "auto"},
	}

	got := SuggestItems("boneless skinless chicken breasts", items, 5)
	if len(got) == 0 {
		t.Fatal("no suggestions for an obvious match")
	}
	if got[0].ItemID != 1 {
		t.Errorf("best match is item %d (%q), want 1", got[0].ItemID, got[0].Name)
	}
	for _, m := range got {
		if m.ItemID == 4 {
			t.Error("an auto placeholder was offered as a match")
		}
		if m.ItemID == 3 {
			t.Error("olive oil was offered as a match for chicken")
		}
	}

	if got := SuggestItems("chicken breast", items, 1); len(got) != 1 {
		t.Errorf("limit not applied: got %d results, want 1", len(got))
	}
}

// Ranking must not reshuffle between identical requests - a list that reorders
// under the cursor is how a person clicks the wrong option.
func TestSuggestIsStable(t *testing.T) {
	items := []*db.Item{
		{ID: 1, Name: "Bell pepper", NormalizedTerm: "bell pepper", Source: "builtin"},
		{ID: 2, Name: "Aell pepper", NormalizedTerm: "aell pepper", Source: "builtin"},
	}
	first := SuggestItems("red bell pepper", items, 5)
	for i := 0; i < 5; i++ {
		again := SuggestItems("red bell pepper", items, 5)
		if len(again) != len(first) {
			t.Fatalf("result count changed: %d then %d", len(first), len(again))
		}
		for j := range first {
			if again[j].ItemID != first[j].ItemID {
				t.Fatalf("order changed at %d: %d then %d", j, first[j].ItemID, again[j].ItemID)
			}
		}
	}
}
