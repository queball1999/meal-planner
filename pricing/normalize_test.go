package pricing_test

import (
	"testing"

	"goeat/pricing"
)

func TestNormalizeCannedAndOes(t *testing.T) {
	cases := map[string]string{
		"tomatoes":              "tomato",
		"Potatoes":              "potato",
		"Cherry tomatoes":       "cherry tomato",
		"Canned diced tomatoes": "canned tomato",
		"diced tomatoes":        "tomato",
		"Canned black beans":    "canned black bean",
		"fresh tomato":          "tomato",
	}
	for in, want := range cases {
		if got := pricing.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
