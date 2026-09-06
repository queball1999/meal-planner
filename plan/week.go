package plan

import "time"

// WeekBounds returns the start and end of the week containing t, where the
// week boundary is determined by startDay ("sunday" or "monday").
// End is always 6 days after start (inclusive). Both are midnight UTC.
// This is the canonical week-boundary function for the app (§11.1, §16.5).
func WeekBounds(t time.Time, startDay string) (start, end time.Time) {
	t = t.UTC().Truncate(24 * time.Hour)
	wd := int(t.Weekday()) // 0=Sunday … 6=Saturday

	var offset int
	if startDay == "monday" {
		// Monday=0 … Sunday=6
		offset = (wd + 6) % 7
	} else {
		// Sunday=0 … Saturday=6
		offset = wd
	}

	start = t.AddDate(0, 0, -offset)
	end = start.AddDate(0, 0, 6)
	return start, end
}
