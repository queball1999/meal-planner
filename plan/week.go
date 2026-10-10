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

// PlanningWeek is the week a "plan my week" with no week named should target:
// the week containing now, unless now is that week's last day, in which case
// it is the week after (rolled is then true). Planning on a Saturday night for
// a Sunday-start week means next week - a plan for a week with hours left in
// it is not what anyone sitting down to plan is after.
func PlanningWeek(now time.Time, startDay string) (start time.Time, rolled bool) {
	start, end := WeekBounds(now, startDay)
	if now.UTC().Truncate(24 * time.Hour).Equal(end) {
		return start.AddDate(0, 0, 7), true
	}
	return start, false
}
