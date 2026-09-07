package web

import (
	"strings"
	"testing"

	"goeat/db"
)

func planPageWithDays(days []calendarDay, members []*db.HouseholdMember, readOnly bool) planPageData {
	return planPageData{
		HasPlan:  true,
		Tab:      "plan",
		Days:     days,
		Members:  members,
		ReadOnly: readOnly,
	}
}

func TestPlanDayRendersPeoplePickerAndStatus(t *testing.T) {
	members := []*db.HouseholdMember{
		{ID: 1, Name: "Sam", PortionFactor: 1.0},
		{ID: 2, Name: "Robin", PortionFactor: 0.5},
	}
	days := []calendarDay{{
		Date:        "2026-01-05",
		DateLabel:   "Mon Jan 5",
		Headcount:   2,
		Status:      db.DayCooking,
		StatusLabel: "cooking",
		Slots:       map[string]calendarSlot{"breakfast": {IsEmpty: true}, "lunch": {IsEmpty: true}, "dinner": {IsEmpty: true}},
		EatingIDs:   map[int64]bool{1: true},
	}}

	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, members, false)})

	for _, want := range []string{
		// Autosaves - the checkmark Save button is gone.
		`data-autosubmit`,
		`class="tooltip-btn"`,
		// One checkbox per member, with the ticked one reflecting EatingIDs.
		`name="member" value="1"`,
		`name="member" value="2"`,
		`action="/plan/days/2026-01-05/status"`,
		`value="eating_out"`,
		`value="skipped"`,
		// The resolution dialog, on the shared modal chrome.
		`id="day-status"`,
		`value="cascade"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan page missing %q", want)
		}
	}
	if strings.Contains(out, `title="Save headcount"`) {
		t.Error("the Save headcount button is still rendered")
	}

	// Sam is eating, Robin is not - the picker must reflect that, not tick
	// everyone.
	samIdx := strings.Index(out, `name="member" value="1"`)
	robinIdx := strings.Index(out, `name="member" value="2"`)
	if !strings.Contains(out[samIdx:samIdx+80], "checked") {
		t.Error("Sam should be ticked")
	}
	if strings.Contains(out[robinIdx:robinIdx+80], "checked") {
		t.Error("Robin should not be ticked")
	}
}

// A household that never set members up keeps the plain number box.
func TestPlanDayFallsBackToHeadcountBox(t *testing.T) {
	days := []calendarDay{{
		Date: "2026-01-05", DateLabel: "Mon Jan 5", Headcount: 4, Status: db.DayCooking,
		Slots: map[string]calendarSlot{"breakfast": {IsEmpty: true}, "lunch": {IsEmpty: true}, "dinner": {IsEmpty: true}},
	}}
	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, false)})

	if !strings.Contains(out, `name="headcount"`) {
		t.Error("no headcount box for a household with no members")
	}
	if strings.Contains(out, `name="member"`) {
		t.Error("member picker rendered with no members")
	}
}

// A non-cooking day reads as one at a glance, and a read-only (past or
// canceled) plan shows the status without offering to change it.
func TestPlanDayShowsNonCookingStatus(t *testing.T) {
	days := []calendarDay{{
		Date: "2026-01-06", DateLabel: "Tue Jan 6", Headcount: 2,
		Status: db.DayEatingOut, StatusLabel: "eating out",
		Slots: map[string]calendarSlot{"breakfast": {IsEmpty: true}, "lunch": {IsEmpty: true}, "dinner": {IsEmpty: true}},
	}}

	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, false)})
	if !strings.Contains(out, "plan-col--off") {
		t.Error("an eating-out day is not visually marked")
	}
	if !strings.Contains(out, `value="eating_out" selected`) {
		t.Error("the eating-out option is not selected")
	}

	ro := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, true)})
	if !strings.Contains(ro, "eating out") {
		t.Error("read-only plan does not show the day status")
	}
	if strings.Contains(ro, `action="/plan/days/2026-01-06/status"`) {
		t.Error("read-only plan offers a status form")
	}
	if strings.Contains(ro, `id="day-status"`) {
		t.Error("read-only plan renders the resolution dialog")
	}
}

func TestDayStatusMessage(t *testing.T) {
	cases := []struct {
		resolution string
		resolved   int
		want       string
	}{
		{"", 0, "Tue Jan 6 marked eating out."},
		{"cascade", 2, "along with 2 day(s) that were living off it"},
		{"clear", 1, "1 leftover meal(s) cleared"},
		// A resolution that changed nothing reads as a plain status change.
		{"cascade", 0, "Tue Jan 6 marked eating out."},
	}
	for _, c := range cases {
		got := dayStatusMessage("2026-01-06", db.DayEatingOut, c.resolved, c.resolution)
		if !strings.Contains(got, c.want) {
			t.Errorf("dayStatusMessage(%q, %d) = %q, want it to contain %q", c.resolution, c.resolved, got, c.want)
		}
	}
}

func TestDayLabelFallsBackToRawString(t *testing.T) {
	if got := dayLabel("2026-01-06"); got != "Tue Jan 6" {
		t.Errorf("dayLabel = %q, want Tue Jan 6", got)
	}
	// Never render a zero date for something unparseable.
	if got := dayLabel("not a date"); got != "not a date" {
		t.Errorf("dayLabel(bad) = %q, want the raw string back", got)
	}
}
