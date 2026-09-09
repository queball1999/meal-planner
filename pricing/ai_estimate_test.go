package pricing

import "testing"

func TestSanitizeEstimatePack(t *testing.T) {
	cases := []struct {
		name         string
		unit         string
		packSize     float64
		wantUnit     string
		wantPackSize float64
	}{
		// The reported bug: "dozen" + 12 means one carton, not 12 dozen.
		{"dozen twelve -> one", "dozen", 12, "dozen", 1},
		{"dozen twenty-four -> two", "dozen", 24, "dozen", 2},
		{"two dozen pack left alone", "dozen", 2, "dozen", 2},
		{"eighteen-count not guessed", "dozen", 18, "dozen", 18},
		{"plain each untouched", "each", 12, "each", 12},
		{"weight unit untouched", "lb", 5, "lb", 5},
		{"fractional dozen untouched", "dozen", 1.5, "dozen", 1.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, p := sanitizeEstimatePack(c.unit, c.packSize)
			if u != c.wantUnit || p != c.wantPackSize {
				t.Errorf("sanitizeEstimatePack(%q, %v) = (%q, %v), want (%q, %v)",
					c.unit, c.packSize, u, p, c.wantUnit, c.wantPackSize)
			}
		})
	}
}
