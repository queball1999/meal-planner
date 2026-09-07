package plan

import (
	"strconv"
	"strings"
)

// vulgarFractions are the single-character fractions that turn up constantly in
// scraped recipes ("1½ cups"). Handled by substitution before parsing rather
// than as a parser case, so "1½" and "1 1/2" take the same path.
var vulgarFractions = map[rune]string{
	'¼': " 1/4", '½': " 1/2", '¾': " 3/4",
	'⅐': " 1/7", '⅑': " 1/9", '⅒': " 1/10",
	'⅓': " 1/3", '⅔': " 2/3",
	'⅕': " 1/5", '⅖': " 2/5", '⅗': " 3/5", '⅘': " 4/5",
	'⅙': " 1/6", '⅚': " 5/6",
	'⅛': " 1/8", '⅜': " 3/8", '⅝': " 5/8", '⅞': " 7/8",
}

// wordNumbers covers the amounts recipes spell out. Only the small ones: past
// about a dozen, recipes use digits.
var wordNumbers = map[string]float64{
	"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "dozen": 12,
	"half": 0.5, "quarter": 0.25,
}

// ParseQuantity turns a catalog recipe's free-text amount into a number.
//
// Catalog recipes store quantity as TEXT because that is what an import gives
// you - "1 1/2", "2-3", "½", "a pinch" - while a plan's meal_ingredients need
// a float to aggregate and price. This is the bridge.
//
// Returns ok=false for text with no number in it at all ("a pinch", "to
// taste", ""). Those are real recipe lines and must not become 0, which would
// silently price a whole ingredient at nothing; the caller decides what to do
// with an unquantified line.
//
// A range takes its low end: "2-3 cloves" buys 2. Shopping short by one clove
// is recoverable, and the alternative - rounding every range up all week - is
// how a budget-first planner quietly overspends.
func ParseQuantity(raw string) (float64, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, false
	}

	var b strings.Builder
	for _, r := range s {
		if frac, ok := vulgarFractions[r]; ok {
			b.WriteString(frac)
			continue
		}
		b.WriteRune(r)
	}
	s = b.String()

	// A range ("2-3", "2 to 3") collapses to its low end. The check for a
	// leading digit keeps this from mangling a hyphenated word.
	for _, sep := range []string{" to ", "–", "—", "-"} {
		if i := strings.Index(s, sep); i > 0 {
			if head := strings.TrimSpace(s[:i]); head != "" && isNumericStart(head) {
				s = head
				break
			}
		}
	}

	fields := strings.Fields(s)
	var total float64
	var got bool

	for _, f := range fields {
		f = strings.Trim(f, ",()")
		if f == "" {
			continue
		}
		// "1 1/2" is one and a half, so parts accumulate.
		if v, ok := parseNumberToken(f); ok {
			total += v
			got = true
			continue
		}
		// The first non-numeric token after a number is a unit or an
		// ingredient word ("2 cups flour"); stop rather than scanning on and
		// picking up a stray number from later in the line.
		if got {
			break
		}
		if v, ok := wordNumbers[f]; ok {
			total += v
			got = true
		}
	}

	if !got {
		return 0, false
	}
	return total, true
}

func isNumericStart(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= '0' && c <= '9' || c == '.'
}

// parseNumberToken handles "2", "0.5", and "1/2".
func parseNumberToken(tok string) (float64, bool) {
	if num, den, ok := strings.Cut(tok, "/"); ok {
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if err1 != nil || err2 != nil || d == 0 {
			return 0, false
		}
		return n / d, true
	}
	v, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
