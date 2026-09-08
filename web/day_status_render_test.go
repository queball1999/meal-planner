package web

import (
	"strings"
	"testing"

	"goeat/db"
	"goeat/plan"
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
		Slots: map[string]calendarSlot{
			"breakfast": {IsEmpty: true},
			"lunch":     {IsEmpty: true},
			// A real meal, so the per-meal status icons (only rendered for a
			// filled slot) actually show up on the page.
			"dinner": {Title: "Chili", MealID: 3, Status: db.DayCooking},
		},
		EatingIDs: map[int64]bool{1: true},
	}}

	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, members, false)})

	for _, want := range []string{
		// Autosaves - the checkmark Save button is gone.
		`data-autosubmit`,
		`class="tooltip-btn"`,
		// One checkbox per member, with the ticked one reflecting EatingIDs.
		`name="member" value="1"`,
		`name="member" value="2"`,
		// Per-meal status icons on the meal card, not a day-level dropdown.
		`data-meal-status-btn`,
		`data-status="eating_out"`,
		`data-status="skipped"`,
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
	if strings.Contains(out, `action="/plan/days/2026-01-05/status"`) {
		t.Error("the day-level status dropdown form is still rendered")
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

// A non-cooking meal reads as one at a glance, and a read-only (past or
// canceled) plan shows the status without offering to change it.
func TestPlanDayShowsNonCookingStatus(t *testing.T) {
	days := []calendarDay{{
		Date: "2026-01-06", DateLabel: "Tue Jan 6", Headcount: 2,
		Status: db.DayCooking, StatusLabel: "cooking",
		Slots: map[string]calendarSlot{
			"breakfast": {IsEmpty: true},
			"lunch":     {IsEmpty: true},
			"dinner":    {Title: "Chili", MealID: 3, Status: db.DayEatingOut},
		},
	}}

	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, false)})
	if !strings.Contains(out, "meal-card--off") {
		t.Error("an eating-out meal is not visually marked")
	}
	if !strings.Contains(out, "meal-status-btn--active") {
		t.Error("the eating-out button is not marked active")
	}
	if !strings.Contains(out, "Eating out</span>") {
		t.Error("the eating-out badge does not show on the card")
	}

	ro := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, true)})
	if !strings.Contains(ro, "meal-card--off") {
		t.Error("read-only plan does not show the meal status")
	}
	if strings.Contains(ro, `data-meal-status-btn`) {
		t.Error("read-only plan offers a status control")
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

// An empty slot has to offer a way out of being empty, and the picker dialog
// has to be on the page for it to open.
func TestPlanEmptySlotOffersFill(t *testing.T) {
	days := []calendarDay{{
		Date: "2026-01-05", DateLabel: "Mon Jan 5", Headcount: 2, Status: db.DayCooking,
		Slots: map[string]calendarSlot{
			"breakfast": {IsEmpty: true},
			"lunch":     {IsEmpty: true},
			"dinner":    {Title: "Chili", MealID: 3},
		},
	}}
	out := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, false)})

	for _, want := range []string{
		`data-fill-slot`,
		`data-slot="breakfast"`,
		`id="pick-recipe"`,
		`id="pick-recipe-list"`,
		`value="replace"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan page missing %q", want)
		}
	}
	// A filled slot is not offered a fill button.
	if strings.Contains(out, `data-slot="dinner"`) {
		t.Error("a filled slot was offered an Add a meal button")
	}

	// A past plan is a record, not an editing surface.
	ro := renderPage(t, "plan", pageData{AppName: "Go Eat", Page: "plan", Data: planPageWithDays(days, nil, true)})
	if strings.Contains(ro, `data-fill-slot`) {
		t.Error("read-only plan offers Add a meal")
	}
}

func TestMealFillMessageNamesUnquantifiedLines(t *testing.T) {
	res := &plan.MaterializeResult{Title: "Chili", Servings: 3}
	if got := mealFillMessage(res, "dinner", "2026-01-05"); strings.Contains(got, "no amount") {
		t.Errorf("clean recipe warned about amounts: %q", got)
	}

	// An unquantified line cannot be priced, so the shopping list will be short
	// by it - the user has to be told which one.
	res.Unquantified = []string{"salt"}
	got := mealFillMessage(res, "dinner", "2026-01-05")
	if !strings.Contains(got, "salt") || !strings.Contains(got, "no amount") {
		t.Errorf("unquantified line not surfaced: %q", got)
	}
	if !strings.Contains(got, "set it on the meal") {
		t.Errorf("singular wording wrong: %q", got)
	}

	res.Unquantified = []string{"salt", "pepper"}
	if got := mealFillMessage(res, "dinner", "2026-01-05"); !strings.Contains(got, "set them on the meal") {
		t.Errorf("plural wording wrong: %q", got)
	}
}

func TestValidSlot(t *testing.T) {
	for _, ok := range []string{"breakfast", "lunch", "dinner"} {
		if !validSlot(ok) {
			t.Errorf("validSlot(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "brunch", "Dinner", "snack"} {
		if validSlot(bad) {
			t.Errorf("validSlot(%q) = true", bad)
		}
	}
}
