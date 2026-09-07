package web

import (
	"strings"
	"testing"

	"goeat/db"
)

func testMembers() []*db.HouseholdMember {
	return []*db.HouseholdMember{
		{ID: 1, Name: "Sam", PortionFactor: 1.0},
		{ID: 2, Name: "Robin", PortionFactor: 0.4, Notes: "toddler - soft textures"},
	}
}

func TestPreferencesPageRendersMembers(t *testing.T) {
	data := buildPreferencesPageData(
		&db.Preferences{},
		nil,
		[]*db.MealSlotHint{{Slot: "dinner"}},
		testMembers(),
		false,
	)
	out := renderPage(t, "preferences", pageData{AppName: "Go Eat", Page: "preferences", Data: data})

	for _, want := range []string{
		"Who eats here",
		"Sam",
		"Robin",
		"toddler - soft textures",
		`data-modal-open="member-add"`,
		`data-modal-open="member-edit-2"`,
		`action="/preferences/members"`,
		`action="/preferences/members/2"`,
		`action="/preferences/members/2/delete"`,
		// 1.0 + 0.4 - the number plans are actually scaled to, shown so the
		// effect of a portion factor is visible before generating anything.
		"1.4 servings",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preferences page missing %q", want)
		}
	}
}

// A household with nobody added still renders, and says what happens instead
// rather than showing an empty table.
func TestPreferencesPageWithNoMembers(t *testing.T) {
	data := buildPreferencesPageData(&db.Preferences{}, nil, nil, nil, false)
	out := renderPage(t, "preferences", pageData{AppName: "Go Eat", Page: "preferences", Data: data})

	if !strings.Contains(out, "No one added yet") {
		t.Error("empty members state not rendered")
	}
	if !strings.Contains(out, `data-modal-open="member-add"`) {
		t.Error("add-person button missing from the empty state")
	}
}

func TestPresetLabelPicksNearest(t *testing.T) {
	cases := map[float64]string{
		0.4:  "Small child",
		0.45: "Small child",
		1.0:  "Standard adult",
		1.3:  "Hearty eater",
		1.5:  "Big eater",
		4.0:  "Big eater", // beyond the presets, clamps to the top one
	}
	for factor, want := range cases {
		if got := presetLabel(factor); got != want {
			t.Errorf("presetLabel(%v) = %q, want %q", factor, got, want)
		}
	}
}

func TestTotalPortions(t *testing.T) {
	if got := totalPortions(testMembers()); got != 1.4 {
		t.Errorf("totalPortions = %v, want 1.4", got)
	}
	if got := totalPortions(nil); got != 0 {
		t.Errorf("totalPortions(nil) = %v, want 0", got)
	}
}

// The setup wizard builds its member rows from a script block that is itself
// a template, so a bad pipeline in there only shows up on execute.
func TestSetupPageRendersPortionPresets(t *testing.T) {
	out := renderPage(t, "setup", pageData{
		AppName: "Go Eat",
		Page:    "setup",
		Data: setupPageData{
			Timezones:      usTimezones,
			PortionPresets: portionPresets,
		},
	})
	for _, want := range []string{
		"Who are you cooking for?",
		// The rows are built client-side, so what the server ships is the
		// builder - these are the field names it assigns.
		`nameInput.name = 'member_name'`,
		`portion.name = 'member_portion'`,
		"Standard adult - one full serving",
		// Without JS there are no member rows at all, so the plain count has
		// to still be submittable.
		`name="household_size"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup page missing %q", want)
		}
	}
}
