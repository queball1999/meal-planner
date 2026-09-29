package pricing

import (
	"math"
	"strconv"
	"strings"
)

// FormatQty renders a quantity for people to read: at most two decimal places,
// trailing zeros dropped - 4 -> "4", 1.5 -> "1.5", 907.1847412751217 -> "907.18".
// Unit conversions (2 lb -> grams) produce long fractions that are exact but
// useless on a list; every quantity shown anywhere goes through here.
func FormatQty(f float64) string {
	s := strconv.FormatFloat(Round2(f), 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// Round2 rounds to two decimal places, FormatQty's precision.
func Round2(f float64) float64 {
	return math.Round(f*100) / 100
}
