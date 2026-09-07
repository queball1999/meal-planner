package web

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// SparkPoint is one observed price, already normalised to a comparable unit.
type SparkPoint struct {
	Cents float64
	At    time.Time
}

// Sparkline is everything a template needs to draw one price series as inline
// SVG.
//
// Inline SVG, no charting library: this is one series of at most a couple of
// dozen points, which does not justify a dependency, and an SVG built here
// inherits the app's own theme tokens instead of shipping a second colour
// system that has to be kept in step with dark mode.
type Sparkline struct {
	// Points is the polyline's "x,y x,y ..." attribute value, in the viewBox's
	// own coordinate space.
	Points string
	// Area closes the same shape down to the baseline, for a soft fill under
	// the line. Empty when there is only one point.
	Area string
	// LastX/LastY position the marker on the most recent reading.
	LastX, LastY float64

	Width, Height float64

	MinLabel  string
	MaxLabel  string
	LastLabel string
	FirstDate string
	LastDate  string

	// Direction is "up", "down" or "flat", comparing the newest reading with
	// the oldest - the summary a reader wants before studying the shape.
	Direction string
	Count     int
}

// sparklinePad keeps the stroke and the end marker inside the viewBox. Without
// it a point at the exact minimum or maximum is drawn half outside and looks
// clipped.
const sparklinePad = 3.0

// BuildSparkline turns a price series into drawable geometry, reporting false
// when there is nothing worth drawing.
//
// Two points is the minimum: a single reading is a number, not a trend, and a
// one-point chart invites a reader to see a shape that is not there.
func BuildSparkline(pts []SparkPoint, width, height float64) (Sparkline, bool) {
	if len(pts) < 2 || width <= 0 || height <= 0 {
		return Sparkline{}, false
	}

	// Defensive: callers read from the database ordered by time, but a
	// mis-ordered series would draw a scribble rather than a line.
	sorted := make([]SparkPoint, len(pts))
	copy(sorted, pts)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })

	minV, maxV := sorted[0].Cents, sorted[0].Cents
	for _, p := range sorted {
		minV = math.Min(minV, p.Cents)
		maxV = math.Max(maxV, p.Cents)
	}

	// A flat series has no range to scale against. Drawing it down the middle
	// is the honest rendering: the price has not moved, and stretching noise
	// to fill the height would imply it had.
	span := maxV - minV
	flat := span < 1e-9

	usableW := width - 2*sparklinePad
	usableH := height - 2*sparklinePad

	var line, area strings.Builder
	var lastX, lastY float64
	for i, p := range sorted {
		x := sparklinePad
		if len(sorted) > 1 {
			x += usableW * float64(i) / float64(len(sorted)-1)
		}
		y := sparklinePad + usableH/2
		if !flat {
			// SVG y grows downward, so a high price has to sit near the top.
			y = sparklinePad + usableH*(1-(p.Cents-minV)/span)
		}
		if i > 0 {
			line.WriteByte(' ')
			area.WriteByte(' ')
		}
		fmt.Fprintf(&line, "%.2f,%.2f", x, y)
		fmt.Fprintf(&area, "%.2f,%.2f", x, y)
		lastX, lastY = x, y
	}

	// Close the fill down the right edge, along the baseline, and back up.
	fmt.Fprintf(&area, " %.2f,%.2f %.2f,%.2f", lastX, height, sparklinePad, height)

	first, last := sorted[0].Cents, sorted[len(sorted)-1].Cents
	direction := "flat"
	switch {
	// A 2% band, so a rounding-level wobble is not reported as a trend.
	case last > first*1.02:
		direction = "up"
	case last < first*0.98:
		direction = "down"
	}

	return Sparkline{
		Points:    line.String(),
		Area:      area.String(),
		LastX:     lastX,
		LastY:     lastY,
		Width:     width,
		Height:    height,
		MinLabel:  centsLabel(minV),
		MaxLabel:  centsLabel(maxV),
		LastLabel: centsLabel(last),
		FirstDate: sorted[0].At.Format("Jan 2"),
		LastDate:  sorted[len(sorted)-1].At.Format("Jan 2"),
		Direction: direction,
		Count:     len(sorted),
	}, true
}

func centsLabel(c float64) string {
	return fmt.Sprintf("$%.2f", c/100)
}

// priceVerdict says whether a price is good, ordinary, or high, against that
// item's own past prices at the same store.
//
// Median, not mean: a single mistyped price (a $40 onion) drags a mean far
// enough to mislabel everything after it, and price history is exactly the
// kind of hand-entered data where that happens.
//
// Compared only against the item's own history and never a cross-store
// average, because two stores' prices for the same thing are not comparable -
// "cheap" against a blend of them would be noise dressed as a signal.
//
// Returns ("", "") when there is not enough history to say anything, which is
// the common case and must read as silence rather than as "ordinary".
func priceVerdict(current float64, prior []float64) (label, class string) {
	// Three prior readings is the floor. With two, one outlier *is* the
	// median.
	if current <= 0 || len(prior) < 3 {
		return "", ""
	}
	med := median(prior)
	if med <= 0 {
		return "", ""
	}
	switch {
	case current <= med*0.9:
		return "good price", "price-verdict--good"
	case current >= med*1.1:
		return "above usual", "price-verdict--high"
	default:
		return "", ""
	}
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := make([]float64, len(vals))
	copy(s, vals)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// unitPrice normalises a recorded price to "cents per unit of amount", so a
// 2 lb pack and a 5 lb pack of the same thing can be compared.
//
// A missing or zero amount means the row predates pack sizes or was entered
// without one; treating it as 1 keeps the raw price rather than dividing by
// zero, which is the same assumption the price editor's own form makes.
func unitPrice(cents int64, amountPerPackage float64) float64 {
	if amountPerPackage <= 0 {
		return float64(cents)
	}
	return float64(cents) / amountPerPackage
}
