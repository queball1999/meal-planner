package plan

import (
	"strings"
	"testing"
	"time"

	"goeat/db"
)

func promptFor(t *testing.T, size int, members []*db.HouseholdMember) string {
	t.Helper()
	hh := &db.Household{HouseholdSize: size, WeeklyBudgetCents: 15000}
	profile := &PreferenceProfile{Members: members}
	start := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	_, user := BuildPrompt(hh, profile, nil, start, start.AddDate(0, 0, 6))
	return user
}

// The whole point of members: a plan for two adults and two toddlers must ask
// for three servings, not four.
func TestPromptUsesPortionSumNotHeadcount(t *testing.T) {
	members := []*db.HouseholdMember{
		{Name: "Ada", PortionFactor: 1.0},
		{Name: "Ben", PortionFactor: 1.0},
		{Name: "Cy", PortionFactor: 0.5},
		{Name: "Di", PortionFactor: 0.5},
	}
	out := promptFor(t, 4, members)

	if !strings.Contains(out, "servings = 3") {
		t.Errorf("prompt does not ask for 3 servings:\n%s", out)
	}
	if strings.Contains(out, "servings = 4") {
		t.Errorf("prompt still asks for a headcount of 4:\n%s", out)
	}
	if !strings.Contains(out, "4 people, eating 3 standard servings") {
		t.Errorf("prompt does not explain the people/servings split:\n%s", out)
	}
}

// Per-member notes are constraints the generator has to see; the household's
// own preferences are separate and already handled.
func TestPromptIncludesMemberNotes(t *testing.T) {
	out := promptFor(t, 2, []*db.HouseholdMember{
		{Name: "Ada", PortionFactor: 1.0, Notes: "no shellfish"},
		{Name: "Cy", PortionFactor: 0.5},
	})

	if !strings.Contains(out, "Ada") || !strings.Contains(out, "no shellfish") {
		t.Errorf("member note missing from prompt:\n%s", out)
	}
	// A member with no note must not produce an empty parenthetical.
	if strings.Contains(out, "()") {
		t.Errorf("empty note rendered as ():\n%s", out)
	}
}

// A household that never set members up has to behave exactly as it did before
// they existed.
func TestPromptFallsBackToHouseholdSize(t *testing.T) {
	out := promptFor(t, 3, nil)

	if !strings.Contains(out, "Household size: 3 people") {
		t.Errorf("no household-size fallback:\n%s", out)
	}
	if !strings.Contains(out, "servings = 3") {
		t.Errorf("fallback does not ask for 3 servings:\n%s", out)
	}
	if strings.Contains(out, "Who eats here") {
		t.Errorf("member block rendered with no members:\n%s", out)
	}
}

func TestTotalPortionsRounds(t *testing.T) {
	cases := []struct {
		members []*db.HouseholdMember
		size    int
		want    int
	}{
		{nil, 4, 4},
		{nil, 0, 1}, // a household size of 0 still has to feed someone
		{[]*db.HouseholdMember{{PortionFactor: 1}, {PortionFactor: 0.5}}, 9, 2},   // 1.5 rounds up
		{[]*db.HouseholdMember{{PortionFactor: 1}, {PortionFactor: 0.4}}, 9, 1},   // 1.4 rounds down
		{[]*db.HouseholdMember{{PortionFactor: 0.4}, {PortionFactor: 0.4}}, 9, 1}, // 0.8 never rounds to 0
	}
	for _, c := range cases {
		p := &PreferenceProfile{Members: c.members}
		if got := p.TotalPortions(c.size); got != c.want {
			t.Errorf("TotalPortions(%d) with %d members = %d, want %d", c.size, len(c.members), got, c.want)
		}
	}
}
