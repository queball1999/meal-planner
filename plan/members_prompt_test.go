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
	_, user := BuildPrompt(hh, profile, nil, start, start.AddDate(0, 0, 6), nil, nil, nil)
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

// A picked recipe or typed request has to actually reach the prompt, and
// take priority language over soft preferences, or "make sure X is in
// there" silently does nothing.
func TestPromptIncludesRequestedMeals(t *testing.T) {
	hh := &db.Household{HouseholdSize: 2, WeeklyBudgetCents: 15000}
	profile := &PreferenceProfile{}
	start := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	_, user := BuildPrompt(hh, profile, nil, start, start.AddDate(0, 0, 6), []string{
		"Grandma's Lasagna (an existing recipe the household picked - use it as specified rather than inventing a substitute)",
		"something with salmon on Friday",
	}, nil, nil)

	if !strings.Contains(user, "Grandma's Lasagna") {
		t.Errorf("requested recipe missing from prompt:\n%s", user)
	}
	if !strings.Contains(user, "something with salmon on Friday") {
		t.Errorf("free-typed request missing from prompt:\n%s", user)
	}
	if !strings.Contains(user, "specifically requested") {
		t.Errorf("prompt does not flag these as the household's own request:\n%s", user)
	}
}

// No request made this week must not add an empty section.
func TestPromptOmitsRequestedMealsSectionWhenEmpty(t *testing.T) {
	out := promptFor(t, 2, nil)
	if strings.Contains(out, "specifically requested") {
		t.Errorf("empty requested-meals section rendered with nothing requested:\n%s", out)
	}
}

// Food typed as "already in the fridge" has to reach the prompt with
// use-it-up language, or the plan buys fresh spinach next to the bag that is
// already wilting.
func TestPromptIncludesOnHandFood(t *testing.T) {
	hh := &db.Household{HouseholdSize: 2, WeeklyBudgetCents: 15000}
	start := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	_, user := BuildPrompt(hh, &PreferenceProfile{}, nil, start, start.AddDate(0, 0, 6), nil,
		[]string{"half a rotisserie chicken", "spinach"}, nil)

	for _, want := range []string{"half a rotisserie chicken", "spinach", "already has in the fridge or pantry", "using these up"} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt missing %q:\n%s", want, user)
		}
	}
}

// Nothing typed must not add an empty on-hand section.
func TestPromptOmitsOnHandSectionWhenEmpty(t *testing.T) {
	out := promptFor(t, 2, nil)
	if strings.Contains(out, "already has in the fridge") {
		t.Errorf("empty on-hand section rendered with nothing on hand:\n%s", out)
	}
}

// A mid-week regenerate restricted to a day subset must tell the LLM plainly
// which days to plan (and how many meals to return) - without this, the LLM
// has no way to know some days already happened and shouldn't be replanned.
func TestPromptFlagsMidWeekDayRestriction(t *testing.T) {
	hh := &db.Household{HouseholdSize: 2, WeeklyBudgetCents: 15000}
	profile := &PreferenceProfile{}
	start := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	_, user := BuildPrompt(hh, profile, nil, start, start.AddDate(0, 0, 6), nil, nil,
		[]string{"wednesday", "thursday", "friday", "saturday"})

	if !strings.Contains(user, "MID-WEEK") {
		t.Errorf("prompt does not flag a restricted day range as mid-week:\n%s", user)
	}
	if !strings.Contains(user, "wednesday, thursday, friday, saturday") {
		t.Errorf("prompt does not list the restricted days:\n%s", user)
	}
	if !strings.Contains(user, "12 meals") {
		t.Errorf("prompt does not ask for the right meal count (4 days x 3 slots):\n%s", user)
	}
}

// The normal full-week case (nil/all-7 days) must not mention any
// restriction - regressing this would confuse every ordinary generation.
func TestPromptOmitsMidWeekFlagForFullWeek(t *testing.T) {
	out := promptFor(t, 2, nil)
	if strings.Contains(out, "MID-WEEK") {
		t.Errorf("full-week prompt should not flag a mid-week restriction:\n%s", out)
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

// The generation prompt has to ask for everything a saved recipe needs, or
// every catalog entry lands with no times and no tags.
func TestSystemPromptAsksForRecipeMetadata(t *testing.T) {
	sys, _ := BuildPrompt(&db.Household{HouseholdSize: 2}, &PreferenceProfile{}, nil,
		time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), nil, nil, nil)

	for _, want := range []string{
		`"prep_minutes"`,
		`"cook_minutes"`,
		`"tags"`,
		// Names loaded with adjectives match nothing in the grocery catalog.
		"plain grocery name",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}
