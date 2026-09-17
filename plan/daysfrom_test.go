package plan

import (
	"reflect"
	"testing"
	"time"
)

// TestDaysFrom covers the day-range math behind the "regenerate mid-week -
// just the remaining days" choice: a mid-week fromDate must trim the leading
// already-happened days, while a fromDate on or before weekStart (the normal
// full-week case, and the only case before this feature existed) must not
// drop anything.
func TestDaysFrom(t *testing.T) {
	sunday := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC) // a Sunday

	cases := []struct {
		name     string
		fromDate time.Time
		want     []string
	}{
		{"same as weekStart", sunday, []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}},
		{"before weekStart clamps to full week", sunday.AddDate(0, 0, -3), []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}},
		{"mid-week wednesday", sunday.AddDate(0, 0, 3), []string{"wednesday", "thursday", "friday", "saturday"}},
		{"last day of the week", sunday.AddDate(0, 0, 6), []string{"saturday"}},
		{"past the week end clamps to last day", sunday.AddDate(0, 0, 10), []string{"saturday"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := daysFrom(sunday, c.fromDate)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("daysFrom(%v) = %v, want %v", c.fromDate, got, c.want)
			}
		})
	}
}
