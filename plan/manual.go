package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"goeat/db"
)

// CreateManual starts an empty plan for a week - no LLM involved - for the
// household to fill slot by slot from its saved recipes. The plan is ready the
// moment it exists, with every day seeded to the whole household eating, so
// the first recipe dropped into it scales the same way a generated one would.
//
// A week that already has a usable plan is left alone and that plan is
// returned (created false): planning by hand must never throw away meals
// someone already chose. Only a failed generation's empty shell is replaced.
func CreateManual(ctx context.Context, store db.Store, householdID int64, weekStart time.Time) (p *db.Plan, created bool, err error) {
	hh, err := store.GetHousehold(ctx, householdID)
	if err != nil {
		return nil, false, fmt.Errorf("get household: %w", err)
	}
	if hh == nil {
		return nil, false, errors.New("household not configured")
	}

	week := weekStart.Format("2006-01-02")
	existing, err := store.GetPlanByWeekStart(ctx, householdID, week)
	if err != nil {
		return nil, false, fmt.Errorf("look up plan: %w", err)
	}
	if existing != nil && existing.Status != "error" {
		return existing, false, nil
	}

	p, err = store.CreatePlan(ctx, db.CreatePlanParams{
		HouseholdID: householdID,
		WeekStart:   week,
		WeekEnd:     weekStart.AddDate(0, 0, 6).Format("2006-01-02"),
		BudgetCents: hh.WeeklyBudgetCents,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create plan: %w", err)
	}
	if err := store.UpdatePlanStatus(ctx, p.ID, "ready"); err != nil {
		return nil, false, fmt.Errorf("update plan status: %w", err)
	}
	p.Status = "ready"

	members, err := store.ListHouseholdMembers(ctx, householdID)
	if err != nil {
		log.Printf("plan: manual plan %d: list members: %v", p.ID, err)
	}
	seedPlanDays(ctx, store, p.ID, weekStart, hh.HouseholdSize, members)

	if err := store.CancelOtherPlansForWeek(ctx, householdID, week, p.ID); err != nil {
		log.Printf("plan: manual plan %d: cancel superseded plans: %v", p.ID, err)
	}
	return p, true, nil
}

// manualLeftoverMaxDayGap is how far back a hand-picked leftover may reach.
// Wider than generation's leftoverMaxDayGap: the model is held to "next day"
// so it cannot stretch one batch across a week unasked, but a cook choosing
// it themselves knows what keeps, and three days is what a fridge allows.
const manualLeftoverMaxDayGap = 3

// before reports whether slot a comes strictly before slot b in the week.
func before(dayA, slotA, dayB, slotB string) bool {
	if dayA != dayB {
		return dayA < dayB
	}
	return slotOrder[slotA] < slotOrder[slotB]
}

// LeftoverSources returns the meals a slot could eat leftovers of: cooked
// earlier in the plan, close enough to still be good, and themselves real
// cooking rather than leftovers or a skipped meal. Nearest first.
func LeftoverSources(meals []*db.Meal, date, slot string) []*db.Meal {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	var out []*db.Meal
	for i := len(meals) - 1; i >= 0; i-- {
		m := meals[i]
		if m.IsLeftover || (m.Status != "" && m.Status != db.DayCooking) || !before(m.Day, m.Slot, date, slot) {
			continue
		}
		cooked, err := time.Parse("2006-01-02", m.Day)
		if err != nil || day.Sub(cooked) > manualLeftoverMaxDayGap*24*time.Hour {
			continue
		}
		out = append(out, m)
	}
	return out
}

// LeftoverParams says which slot eats which earlier meal's leftovers.
type LeftoverParams struct {
	PlanID       int64
	SourceMealID int64
	Date         string
	Slot         string
	Portions     float64 // what the day is feeding, as for MaterializeParams
}

// LeftoverResult reports the leftover meal and what it cost the source.
type LeftoverResult struct {
	MealID      int64
	Title       string
	Servings    int
	SourceTitle string
	// ExtraCooked is how many more portions the source meal now cooks to feed
	// this one; 0 when it already had that much to spare.
	ExtraCooked int
}

// AddLeftoverMeal fills a slot with the leftovers of an earlier meal and makes
// that meal cook enough to cover it - the by-hand counterpart of what
// PlanLeftovers infers for a generated plan.
//
// The source only grows by what it is short: a meal already cooking a surplus
// nobody is eating feeds the new slot from that first. Any meal already in the
// slot is replaced, as with MaterializeRecipe.
func AddLeftoverMeal(ctx context.Context, store db.Store, p LeftoverParams) (*LeftoverResult, error) {
	src, err := store.GetMealByID(ctx, p.SourceMealID)
	if err != nil {
		return nil, fmt.Errorf("get source meal: %w", err)
	}
	if src == nil || src.PlanID != p.PlanID {
		return nil, fmt.Errorf("meal %d is not on this plan", p.SourceMealID)
	}
	if src.IsLeftover {
		return nil, errors.New("that meal is leftovers itself")
	}
	if !before(src.Day, src.Slot, p.Date, p.Slot) {
		return nil, errors.New("leftovers have to come from an earlier meal")
	}

	servings := int(math.Round(p.Portions))
	if servings < 1 {
		servings = 1
	}

	if err := replaceSlot(ctx, store, p.PlanID, p.Date, p.Slot); err != nil {
		return nil, err
	}

	deps, err := store.ListLeftoversSourcedFromMeal(ctx, src.ID)
	if err != nil {
		return nil, fmt.Errorf("list leftovers: %w", err)
	}
	spare := src.CookedPortions - src.Servings
	for _, d := range deps {
		spare -= d.Servings
	}
	if spare < 0 {
		spare = 0
	}
	extra := servings - spare
	if extra < 0 {
		extra = 0
	}
	if err := store.ExtendMealCooked(ctx, src.ID, extra); err != nil {
		return nil, fmt.Errorf("cook extra: %w", err)
	}

	// "Leftover" in the title is how the rest of the app recognises one
	// (db.IsLeftoverTitle).
	title := "Leftover " + src.Title
	meal, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID:         p.PlanID,
		Day:            p.Date,
		Slot:           p.Slot,
		Title:          title,
		Effort:         "quick",
		Servings:       servings,
		CookedPortions: servings,
	})
	if err != nil {
		return nil, fmt.Errorf("create meal: %w", err)
	}
	if err := store.UpdateMealLeftover(ctx, meal.ID, true, &src.ID); err != nil {
		return nil, fmt.Errorf("link leftover: %w", err)
	}

	steps, _ := json.Marshal([]string{"Reheat the " + src.Title + " left over from " + dayName(src.Day) + "."})
	if err := store.CreateMealRecipe(ctx, db.CreateMealRecipeParams{
		MealID:    meal.ID,
		StepsJSON: string(steps),
		Servings:  servings,
	}); err != nil {
		log.Printf("leftover: steps for meal %d: %v", meal.ID, err)
	}

	return &LeftoverResult{
		MealID: meal.ID, Title: title, Servings: servings,
		SourceTitle: src.Title, ExtraCooked: extra,
	}, nil
}

// dayName is the capitalised weekday of a YYYY-MM-DD, for a sentence.
func dayName(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Weekday().String()
}

// What a move does when its destination already holds a meal, and what it
// does to leftover meals it would strand.
const (
	MoveSwap    = "swap"    // the two meals trade places
	MoveReplace = "replace" // the meal already there is removed

	MoveUnlink = "unlink" // stranded leftovers stay, tied to no meal
	MoveClear  = "clear"  // stranded leftovers are removed
)

// MoveImpact is what a move would do beyond relocating one meal.
type MoveImpact struct {
	// Displaced is the meal already in the destination, nil if it is empty.
	Displaced *db.Meal
	// Stranded are the leftover meals the move would leave with nothing to
	// eat: served before the meal they are leftovers of is cooked, or left
	// behind by a source the move replaces.
	Stranded []*db.Meal
}

// PlanMove works out what moving one meal to (date, slot) would disturb,
// without changing anything. occupied is MoveSwap or MoveReplace and only
// matters when the destination holds a meal; "" means MoveSwap.
//
// Only links the move itself touches are reported. A leftover that was
// already out of order before it is somebody else's problem, and blaming this
// move for it would send the user hunting for a mistake they did not make.
func PlanMove(meals []*db.Meal, mealID int64, date, slot, occupied string) (MoveImpact, error) {
	var impact MoveImpact
	var mover *db.Meal
	for _, m := range meals {
		if m.ID == mealID {
			mover = m
		}
	}
	if mover == nil {
		return impact, fmt.Errorf("meal %d is not on this plan", mealID)
	}
	for _, m := range meals {
		if m.ID != mealID && m.Day == date && m.Slot == slot {
			impact.Displaced = m
		}
	}

	type pos struct{ day, slot string }
	at := make(map[int64]pos, len(meals))
	for _, m := range meals {
		at[m.ID] = pos{m.Day, m.Slot}
	}
	at[mover.ID] = pos{date, slot}
	touched := map[int64]bool{mover.ID: true}
	if d := impact.Displaced; d != nil {
		touched[d.ID] = true
		if occupied == MoveReplace {
			delete(at, d.ID)
		} else {
			at[d.ID] = pos{mover.Day, mover.Slot}
		}
	}

	for _, m := range meals {
		if !m.IsLeftover || m.LeftoverSourceMealID == nil {
			continue
		}
		srcID := *m.LeftoverSourceMealID
		if !touched[m.ID] && !touched[srcID] {
			continue
		}
		self, here := at[m.ID]
		if !here {
			continue // removed by the move itself
		}
		src, cooked := at[srcID]
		if !cooked || !before(src.day, src.slot, self.day, self.slot) {
			impact.Stranded = append(impact.Stranded, m)
		}
	}
	return impact, nil
}

// MoveParams says where a meal goes and how to settle what it disturbs.
type MoveParams struct {
	MealID    int64
	Date      string
	Slot      string
	Occupied  string // MoveSwap (default) | MoveReplace
	Leftovers string // MoveUnlink (default) | MoveClear
}

// MoveResult reports what a move did.
type MoveResult struct {
	Title     string
	FromDay   string
	FromSlot  string
	Displaced string // title of the meal that was in the destination, "" if none
	Replaced  bool   // Displaced was removed rather than swapped
	Stranded  int    // leftover meals unlinked or cleared
}

// ErrMealLocked is returned when a move would remove a locked meal.
var ErrMealLocked = errors.New("that meal is locked")

// MoveMeal relocates a meal within its plan and settles what PlanMove said it
// would disturb - the drag-and-drop on the plan board.
func MoveMeal(ctx context.Context, store db.Store, p MoveParams) (*MoveResult, error) {
	mover, err := store.GetMealByID(ctx, p.MealID)
	if err != nil {
		return nil, fmt.Errorf("get meal: %w", err)
	}
	if mover == nil {
		return nil, fmt.Errorf("meal %d not found", p.MealID)
	}
	res := &MoveResult{Title: mover.Title, FromDay: mover.Day, FromSlot: mover.Slot}
	if mover.Day == p.Date && mover.Slot == p.Slot {
		return res, nil
	}

	meals, err := store.ListMealsByPlan(ctx, mover.PlanID)
	if err != nil {
		return nil, fmt.Errorf("list meals: %w", err)
	}
	impact, err := PlanMove(meals, p.MealID, p.Date, p.Slot, p.Occupied)
	if err != nil {
		return nil, err
	}

	if d := impact.Displaced; d != nil {
		res.Displaced = d.Title
		if p.Occupied == MoveReplace {
			if d.Locked {
				return nil, ErrMealLocked
			}
			// DeleteMeal turns anything eating its leftovers back into an
			// ordinary meal; those are in impact.Stranded and settled below.
			if err := store.DeleteMeal(ctx, d.ID); err != nil {
				return nil, fmt.Errorf("replace meal: %w", err)
			}
			res.Replaced = true
		}
	}

	// store.MoveMeal swaps whatever still occupies the destination.
	if _, err := store.MoveMeal(ctx, p.MealID, p.Date, p.Slot); err != nil {
		return nil, fmt.Errorf("move meal: %w", err)
	}

	for _, m := range impact.Stranded {
		if p.Leftovers == MoveClear {
			err = store.DeleteMeal(ctx, m.ID)
		} else {
			// Still leftovers, so they stay off the shopping list - the same
			// state SwapIgnore leaves one in.
			err = store.UpdateMealLeftover(ctx, m.ID, true, nil)
		}
		if err != nil {
			log.Printf("move: settle stranded leftover meal %d: %v", m.ID, err)
			continue
		}
		res.Stranded++
	}
	return res, nil
}

// ValidMoveChoice reports whether occupied and leftovers are choices MoveMeal
// accepts.
func ValidMoveChoice(occupied, leftovers string) bool {
	switch occupied {
	case "", MoveSwap, MoveReplace:
	default:
		return false
	}
	switch leftovers {
	case "", MoveUnlink, MoveClear:
		return true
	}
	return false
}
