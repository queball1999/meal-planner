package plan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"goeat/db"
)

// What ReviewPlan can find wrong with a plan.
const (
	// IssueEmptySlot is a meal missing from a day that is being cooked.
	IssueEmptySlot = "empty_slot"
	// IssueLeftoverNoSource is a leftovers meal tied to no earlier meal.
	IssueLeftoverNoSource = "leftover_no_source"
	// IssueLeftoverTooEarly is a leftovers meal eaten before the meal it is
	// leftovers of is cooked.
	IssueLeftoverTooEarly = "leftover_too_early"
	// IssueLeftoverStale is a leftovers meal more than manualLeftoverMaxDayGap
	// days after the meal it is leftovers of.
	IssueLeftoverStale = "leftover_stale"
	// IssueLeftoverSourceOff is a leftovers meal whose source is on a day, or
	// is itself a meal, that is not being cooked.
	IssueLeftoverSourceOff = "leftover_source_off"
	// IssueCookShort is a meal that does not cook enough to cover the
	// leftovers planned from it. The only kind with one right answer
	// (FixCookShort); the rest need someone to choose a dish.
	IssueCookShort = "cook_short"
)

// Issue is one thing wrong with a plan.
type Issue struct {
	Kind string
	// Day and Slot are where the problem is: the empty slot, the leftovers
	// meal, or for IssueCookShort the meal that cooks too little.
	Day  string
	Slot string
	// Meal is the meal in that slot; nil for IssueEmptySlot.
	Meal *db.Meal
	// Source is the meal a leftovers issue is tied to, when it has one.
	Source *db.Meal
	// Short is how many portions an IssueCookShort meal is short by.
	Short int
}

// String is the issue as a sentence, for the assistant and the person alike.
func (i Issue) String() string {
	at := weekdayName(i.Day) + " " + i.Slot
	switch i.Kind {
	case IssueEmptySlot:
		return at + " has no meal"
	case IssueLeftoverNoSource:
		return fmt.Sprintf("%q on %s is leftovers, but is not tied to any meal cooked before it", i.Meal.Title, at)
	case IssueLeftoverTooEarly:
		return fmt.Sprintf("%q on %s is eaten before %q is cooked on %s %s",
			i.Meal.Title, at, i.Source.Title, weekdayName(i.Source.Day), i.Source.Slot)
	case IssueLeftoverStale:
		return fmt.Sprintf("%q on %s is leftovers of %q from %s %s, more than %d days earlier",
			i.Meal.Title, at, i.Source.Title, weekdayName(i.Source.Day), i.Source.Slot, manualLeftoverMaxDayGap)
	case IssueLeftoverSourceOff:
		return fmt.Sprintf("%q on %s is leftovers of %q on %s %s, which is not being cooked",
			i.Meal.Title, at, i.Source.Title, weekdayName(i.Source.Day), i.Source.Slot)
	case IssueCookShort:
		return fmt.Sprintf("%q on %s cooks %d too few portions for the leftovers planned from it",
			i.Meal.Title, at, i.Short)
	}
	return at
}

// ReviewPlan checks a plan for the contradictions a week picks up as it is
// edited: slots left empty, leftovers with nothing to reheat, and meals that
// do not cook enough for the leftovers eaten from them. In week order.
//
// today (YYYY-MM-DD) leaves alone what has already happened - nobody wants
// last Tuesday's lunch filled in, and the days before a mid-week plan began
// are empty on purpose. "" checks the whole week.
func ReviewPlan(p *db.Plan, meals []*db.Meal, days []*db.PlanDay, today string) []Issue {
	dayOff := map[string]bool{}
	for _, d := range days {
		if d.Status != "" && d.Status != db.DayCooking {
			dayOff[d.Date] = true
		}
	}
	// off: nobody is cooking or eating this meal.
	off := func(m *db.Meal) bool {
		return dayOff[m.Day] || (m.Status != "" && m.Status != db.DayCooking)
	}
	byID := make(map[int64]*db.Meal, len(meals))
	filled := map[string]bool{}
	for _, m := range meals {
		byID[m.ID] = m
		filled[m.Day+" "+m.Slot] = true
	}

	var issues []Issue

	if start, err := time.Parse("2006-01-02", p.WeekStart); err == nil {
		for i := 0; i < 7; i++ {
			date := start.AddDate(0, 0, i).Format("2006-01-02")
			if date > p.WeekEnd || date < today || dayOff[date] {
				continue
			}
			for _, slot := range []string{"breakfast", "lunch", "dinner"} {
				if !filled[date+" "+slot] {
					issues = append(issues, Issue{Kind: IssueEmptySlot, Day: date, Slot: slot})
				}
			}
		}
	}

	// eaten is the portions of leftovers soundly planned from each meal.
	eaten := map[int64]int{}
	for _, m := range meals {
		if !(m.IsLeftover || db.IsLeftoverTitle(m.Title)) || m.Day < today || off(m) {
			continue
		}
		is := Issue{Day: m.Day, Slot: m.Slot, Meal: m}
		if m.LeftoverSourceMealID != nil {
			is.Source = byID[*m.LeftoverSourceMealID]
		}
		switch {
		case is.Source == nil:
			is.Kind = IssueLeftoverNoSource
		case !before(is.Source.Day, is.Source.Slot, m.Day, m.Slot):
			is.Kind = IssueLeftoverTooEarly
		case off(is.Source):
			is.Kind = IssueLeftoverSourceOff
		case daysBetween(is.Source.Day, m.Day) > manualLeftoverMaxDayGap:
			is.Kind = IssueLeftoverStale
		default:
			eaten[is.Source.ID] += m.Servings
			continue
		}
		issues = append(issues, is)
	}

	for _, m := range meals {
		short := eaten[m.ID] - (m.CookedPortions - m.Servings)
		if short > 0 && m.Day >= today {
			issues = append(issues, Issue{Kind: IssueCookShort, Day: m.Day, Slot: m.Slot, Meal: m, Short: short})
		}
	}

	sort.SliceStable(issues, func(a, b int) bool {
		return before(issues[a].Day, issues[a].Slot, issues[b].Day, issues[b].Slot)
	})
	return issues
}

// FixCookShort makes every IssueCookShort meal cook what it is short by, and
// returns the issues it settled and the ones still open. The rest of the
// kinds come back untouched: they need a dish chosen, which is not a sum.
func FixCookShort(ctx context.Context, store db.Store, issues []Issue) (fixed, open []Issue, err error) {
	for _, is := range issues {
		if is.Kind != IssueCookShort {
			open = append(open, is)
			continue
		}
		if err := store.ExtendMealCooked(ctx, is.Meal.ID, is.Short); err != nil {
			return fixed, open, fmt.Errorf("cook more of meal %d: %w", is.Meal.ID, err)
		}
		fixed = append(fixed, is)
	}
	return fixed, open, nil
}

// LeftoverFix is one slot FixWithLeftovers filled.
type LeftoverFix struct {
	Issue  Issue
	Source *db.Meal
	Result *LeftoverResult
}

// FixWithLeftovers settles the lunches that have one obvious answer in a
// household that plans leftovers: a lunch with no meal, or a lunch of
// leftovers with nothing sound to reheat, becomes the leftovers of the dinner
// cooked the day before (spec §4.3, "the next day's lunch"), and that dinner
// cooks enough to cover it. It returns what it filled and the issues still
// open - a lunch whose day before has no dinner being cooked, a locked meal,
// and every breakfast and dinner, which need a dish chosen.
//
// defaultPortions is what a day feeds when it has no portions of its own.
func FixWithLeftovers(ctx context.Context, store db.Store, planID int64, issues []Issue, defaultPortions float64) (fixed []LeftoverFix, open []Issue, err error) {
	meals, err := store.ListMealsByPlan(ctx, planID)
	if err != nil {
		return nil, issues, fmt.Errorf("list meals: %w", err)
	}
	days, err := store.ListPlanDays(ctx, planID)
	if err != nil {
		return nil, issues, fmt.Errorf("list days: %w", err)
	}
	dayOff := map[string]bool{}
	portions := map[string]float64{}
	for _, d := range days {
		if d.Status != "" && d.Status != db.DayCooking {
			dayOff[d.Date] = true
		}
		if d.Portions > 0 {
			portions[d.Date] = d.Portions
		}
	}

	for _, is := range issues {
		src := nextDayLeftoverSource(is, meals, dayOff)
		if src == nil {
			open = append(open, is)
			continue
		}
		feeds := defaultPortions
		if p, ok := portions[is.Day]; ok {
			feeds = p
		}
		res, err := AddLeftoverMeal(ctx, store, LeftoverParams{
			PlanID: planID, SourceMealID: src.ID, Date: is.Day, Slot: is.Slot, Portions: feeds,
		})
		if err != nil {
			return fixed, append(open, is), fmt.Errorf("leftovers for %s %s: %w", is.Day, is.Slot, err)
		}
		fixed = append(fixed, LeftoverFix{Issue: is, Source: src, Result: res})
	}
	return fixed, open, nil
}

// nextDayLeftoverSource is the dinner cooked the day before a lunch that
// FixWithLeftovers may fill, or nil when the issue is not one it settles.
func nextDayLeftoverSource(is Issue, meals []*db.Meal, dayOff map[string]bool) *db.Meal {
	if is.Slot != "lunch" {
		return nil
	}
	switch is.Kind {
	case IssueEmptySlot:
	case IssueLeftoverNoSource, IssueLeftoverTooEarly, IssueLeftoverStale, IssueLeftoverSourceOff:
		if is.Meal == nil || is.Meal.Locked {
			return nil
		}
	default:
		return nil
	}
	day, err := time.Parse("2006-01-02", is.Day)
	if err != nil {
		return nil
	}
	eve := day.AddDate(0, 0, -1).Format("2006-01-02")
	if dayOff[eve] {
		return nil
	}
	for _, m := range LeftoverSources(meals, is.Day, is.Slot) {
		if m.Day == eve && m.Slot == "dinner" && !db.IsLeftoverTitle(m.Title) {
			return m
		}
	}
	return nil
}
