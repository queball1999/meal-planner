package plan

import (
	"math"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"2", 2},
		{"0.5", 0.5},
		{".5", 0.5},
		{"1/2", 0.5},
		{"1 1/2", 1.5}, // mixed number accumulates
		{"½", 0.5},     // vulgar fraction
		{"1½", 1.5},    // digit glued to a vulgar fraction
		{"2-3", 2},     // a range takes its low end
		{"2 to 3", 2},  // ...however it is written
		{"2–3", 2},     // en dash
		{"one", 1},     // spelled out
		{"a", 1},       // "a pinch of salt" is still one pinch
		{"two", 2},     // ...
		{"dozen", 12},  //
		{"  3  ", 3},   // surrounding space
		{"2 cups", 2},  // trailing unit is not part of the number
		{"3 large eggs", 3},
		// The unit stops the scan, so a number later in the line is not
		// mistaken for part of the amount.
		{"1 can (14 oz)", 1},
	}
	for _, c := range cases {
		got, ok := ParseQuantity(c.in)
		if !ok {
			t.Errorf("ParseQuantity(%q) returned ok=false, want %v", c.in, c.want)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ParseQuantity(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Text with no number must not become 0: that would silently price a whole
// ingredient at nothing. The caller decides what an unquantified line means.
func TestParseQuantityRejectsUnquantified(t *testing.T) {
	for _, in := range []string{"", "   ", "to taste", "as needed", "salt"} {
		if v, ok := ParseQuantity(in); ok {
			t.Errorf("ParseQuantity(%q) = %v, ok=true; want ok=false", in, v)
		}
	}
}

func TestParseQuantityBadFraction(t *testing.T) {
	// A zero denominator must not divide by zero or return +Inf.
	if v, ok := ParseQuantity("1/0"); ok {
		t.Errorf("ParseQuantity(1/0) = %v, ok=true; want ok=false", v)
	}
}
