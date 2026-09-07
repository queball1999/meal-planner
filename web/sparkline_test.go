package web

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func series(cents ...float64) []SparkPoint {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]SparkPoint, len(cents))
	for i, c := range cents {
		out[i] = SparkPoint{Cents: c, At: base.AddDate(0, 0, i*7)}
	}
	return out
}

func TestBuildSparklineGeometry(t *testing.T) {
	s, ok := BuildSparkline(series(100, 200, 150), 100, 40)
	if !ok {
		t.Fatal("a three-point series produced no chart")
	}
	if s.Count != 3 {
		t.Errorf("Count = %d, want 3", s.Count)
	}
	if s.MinLabel != "$1.00" || s.MaxLabel != "$2.00" || s.LastLabel != "$1.50" {
		t.Errorf("labels = %q/%q/%q", s.MinLabel, s.MaxLabel, s.LastLabel)
	}

	pts := strings.Fields(s.Points)
	if len(pts) != 3 {
		t.Fatalf("got %d points, want 3", len(pts))
	}
	// Every coordinate has to sit inside the viewBox, or the stroke and the
	// end marker are drawn half outside and look clipped.
	for _, p := range pts {
		x, y := xyOf(t, p)
		if x < 0 || x > 100 || y < 0 || y > 40 {
			t.Errorf("point %q falls outside the 100x40 viewBox", p)
		}
	}

	// SVG y grows downward, so the highest price must have the smallest y.
	_, y0 := xyOf(t, pts[0])
	_, y1 := xyOf(t, pts[1])
	_, y2 := xyOf(t, pts[2])
	if !(y1 < y2 && y2 < y0) {
		t.Errorf("y ordering wrong for 100/200/150: %v %v %v", y0, y1, y2)
	}
}

func TestBuildSparklineNeedsTwoPoints(t *testing.T) {
	// One reading is a number, not a trend; a one-point chart invites the
	// reader to see a shape that is not there.
	if _, ok := BuildSparkline(series(100), 100, 40); ok {
		t.Error("a single point produced a chart")
	}
	if _, ok := BuildSparkline(nil, 100, 40); ok {
		t.Error("an empty series produced a chart")
	}
	if _, ok := BuildSparkline(series(100, 200), 0, 40); ok {
		t.Error("a zero-width chart was built")
	}
}

// A price that has not moved must be drawn flat, not stretched to fill the
// height - that would turn no change into a dramatic shape.
func TestBuildSparklineFlatSeries(t *testing.T) {
	s, ok := BuildSparkline(series(250, 250, 250), 100, 40)
	if !ok {
		t.Fatal("a flat series produced no chart")
	}
	if s.Direction != "flat" {
		t.Errorf("Direction = %q, want flat", s.Direction)
	}
	pts := strings.Fields(s.Points)
	_, y0 := xyOf(t, pts[0])
	_, y2 := xyOf(t, pts[2])
	if math.Abs(y0-y2) > 1e-6 {
		t.Errorf("a flat series is not level: %v vs %v", y0, y2)
	}
}

func TestSparklineDirection(t *testing.T) {
	cases := []struct {
		name string
		in   []SparkPoint
		want string
	}{
		{"rising", series(100, 150), "up"},
		{"falling", series(150, 100), "down"},
		// Inside the 2% band: a rounding-level wobble is not a trend.
		{"noise", series(100, 101), "flat"},
	}
	for _, c := range cases {
		s, ok := BuildSparkline(c.in, 100, 40)
		if !ok {
			t.Fatalf("%s: no chart", c.name)
		}
		if s.Direction != c.want {
			t.Errorf("%s: Direction = %q, want %q", c.name, s.Direction, c.want)
		}
	}
}

func TestPriceVerdict(t *testing.T) {
	usual := []float64{300, 310, 290, 305}

	if label, _ := priceVerdict(250, usual); label != "good price" {
		t.Errorf("250 against ~300 = %q, want good price", label)
	}
	if label, _ := priceVerdict(400, usual); label != "above usual" {
		t.Errorf("400 against ~300 = %q, want above usual", label)
	}
	// Ordinary is silence, not a label - most prices are ordinary and a badge
	// on every line says nothing.
	if label, _ := priceVerdict(300, usual); label != "" {
		t.Errorf("300 against ~300 = %q, want no label", label)
	}
}

// Too little history has to read as silence, not as "ordinary" - with two
// prior readings, one outlier *is* the median.
func TestPriceVerdictNeedsHistory(t *testing.T) {
	for _, prior := range [][]float64{nil, {300}, {300, 310}} {
		if label, _ := priceVerdict(100, prior); label != "" {
			t.Errorf("verdict %q from %d prior readings", label, len(prior))
		}
	}
	if label, _ := priceVerdict(0, []float64{1, 2, 3}); label != "" {
		t.Errorf("a zero current price produced %q", label)
	}
}

// Median, not mean: one mistyped price must not relabel everything after it.
func TestPriceVerdictResistsOneOutlier(t *testing.T) {
	// A $40 onion among $3 ones. A mean would be ~$12 and would call the next
	// normal $3 price a bargain.
	withTypo := []float64{300, 310, 4000, 290, 305}
	if label, _ := priceVerdict(300, withTypo); label != "" {
		t.Errorf("a normal price next to one typo was labelled %q", label)
	}
}

func TestUnitPrice(t *testing.T) {
	// A 2 lb pack at $6 is $3/lb; a 5 lb pack at $12 is $2.40/lb. Comparing
	// the raw prices would call the bigger pack the more expensive one.
	if got := unitPrice(600, 2); math.Abs(got-300) > 1e-9 {
		t.Errorf("unitPrice(600, 2) = %v, want 300", got)
	}
	if got := unitPrice(1200, 5); math.Abs(got-240) > 1e-9 {
		t.Errorf("unitPrice(1200, 5) = %v, want 240", got)
	}
	// A row with no pack size predates the column; keep the raw price rather
	// than dividing by zero.
	if got := unitPrice(600, 0); got != 600 {
		t.Errorf("unitPrice(600, 0) = %v, want 600", got)
	}
}

// xyOf parses one "x,y" pair out of a polyline points attribute.
func xyOf(t *testing.T, pair string) (float64, float64) {
	t.Helper()
	var x, y float64
	if _, err := fmt.Sscanf(pair, "%f,%f", &x, &y); err != nil {
		t.Fatalf("unparseable point %q: %v", pair, err)
	}
	return x, y
}
