package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"goeat/db"
	"goeat/plan"
)

var slotNames = []string{"breakfast", "lunch", "dinner"}

// currentPlan loads the household's active plan, with a message the model can
// act on when there is none.
func currentPlan(ctx context.Context, s *Session) (*db.Plan, error) {
	if s.PlanID != 0 {
		p, err := s.Store.GetPlanByID(ctx, s.PlanID)
		if err != nil {
			return nil, err
		}
		// The id is the caller's, but the household check stays here with
		// every other one (Session).
		if p == nil || p.HouseholdID != s.HouseholdID {
			return nil, fmt.Errorf("that plan is not this household's")
		}
		return p, nil
	}
	p, err := s.Store.GetLatestPlan(ctx, s.HouseholdID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("this household has no meal plan yet - generate one first")
	}
	return p, nil
}

// resolveDay turns whatever the model said into a date inside the plan week.
//
// Models say "monday" far more often than "2026-01-05", and a tool that only
// took ISO dates would push date arithmetic onto the model - which is exactly
// where it gets it wrong. Both are accepted; a weekday resolves within the
// current plan's own week, which is the week the user is looking at.
func resolveDay(p *db.Plan, day string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(day))
	if d == "" {
		return "", fmt.Errorf("which day?")
	}
	// A weekday name resolves within the plan's own week (plan.DayInWeek),
	// which starts on whichever weekday WEEK_START_DAY says.
	if _, named := plan.DayInWeek(time.Time{}, d); named {
		start, err := time.Parse("2006-01-02", p.WeekStart)
		if err != nil {
			return "", fmt.Errorf("this plan has an unreadable week start (%q)", p.WeekStart)
		}
		date, _ := plan.DayInWeek(start, d)
		return date.Format("2006-01-02"), nil
	}
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return "", fmt.Errorf("%q is not a day - use a weekday name or YYYY-MM-DD", day)
	}
	if t.Format("2006-01-02") < p.WeekStart || t.Format("2006-01-02") > p.WeekEnd {
		return "", fmt.Errorf("%s is outside this plan's week (%s to %s)", d, p.WeekStart, p.WeekEnd)
	}
	return t.Format("2006-01-02"), nil
}

func validSlotName(slot string) bool {
	s := strings.ToLower(strings.TrimSpace(slot))
	for _, v := range slotNames {
		if v == s {
			return true
		}
	}
	return false
}

// findMeal locates a meal by id or by title within the current plan.
//
// Title matching exists because a user says "move the chicken quesadillas",
// not "move meal 47", and forcing the model to read the whole plan first just
// to translate one name into an id wastes a round trip on every request. The
// match is case-insensitive and accepts a unique substring; an ambiguous name
// is an error naming the candidates rather than a guess, since guessing which
// of two meals someone meant and then moving it is not recoverable.
func findMeal(ctx context.Context, s *Session, planID int64, id int64, title string) (*db.Meal, error) {
	meals, err := s.Store.ListMealsByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if id > 0 {
		for _, m := range meals {
			if m.ID == id {
				return m, nil
			}
		}
		return nil, fmt.Errorf("no meal with id %d in the current plan", id)
	}

	needle := strings.ToLower(strings.TrimSpace(title))
	if needle == "" {
		return nil, fmt.Errorf("name a meal, by title or id")
	}
	var exact, partial []*db.Meal
	for _, m := range meals {
		lt := strings.ToLower(m.Title)
		if lt == needle {
			exact = append(exact, m)
		} else if strings.Contains(lt, needle) {
			partial = append(partial, m)
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = partial
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no meal called %q in the current plan", title)
	case 1:
		return hits[0], nil
	default:
		var names []string
		for _, m := range hits {
			names = append(names, fmt.Sprintf("%q on %s %s", m.Title, m.Day, m.Slot))
		}
		return nil, fmt.Errorf("%q matches several meals (%s) - say which one",
			title, strings.Join(names, ", "))
	}
}

// findLeftoverSource picks the meal to reheat out of the ones a slot could
// eat leftovers of, by id or by title.
//
// The title is matched with any "leftover" word dropped, because that is how
// the request arrives ("leftover seasoned chicken tacos") and no meal on the
// plan is called that yet. Same rules as findMeal otherwise: exact beats a
// substring, and an ambiguous name is an error rather than a guess.
func findLeftoverSource(sources []*db.Meal, id int64, title string) (*db.Meal, error) {
	if id > 0 {
		for _, m := range sources {
			if m.ID == id {
				return m, nil
			}
		}
		return nil, fmt.Errorf("meal %d cannot be leftovers", id)
	}
	var words []string
	for _, w := range strings.Fields(strings.ToLower(title)) {
		if w != "leftover" && w != "leftovers" && w != "of" {
			words = append(words, w)
		}
	}
	needle := strings.Join(words, " ")
	if needle == "" {
		return nil, fmt.Errorf("name the meal to reheat")
	}
	var exact, partial []*db.Meal
	for _, m := range sources {
		lt := strings.ToLower(m.Title)
		if lt == needle {
			exact = append(exact, m)
		} else if strings.Contains(lt, needle) {
			partial = append(partial, m)
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = partial
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("%q cannot be leftovers", title)
	case 1:
		return hits[0], nil
	default:
		return nil, fmt.Errorf("%q matches several meals - say which one", title)
	}
}

// moveSummary says what a move did beyond relocating the one meal: which
// leftovers went with it, and which were left with nothing to reheat. The
// second is addressed to the model as much as the user - it is the cue to fix
// the plan or say so, rather than report a clean move.
func moveSummary(res *plan.MoveResult) string {
	var b strings.Builder
	if len(res.Followed) > 0 {
		var parts []string
		for _, m := range res.Followed {
			parts = append(parts, fmt.Sprintf("%q to %s %s", m.Title, weekdayOf(m.Day), m.Slot))
		}
		fmt.Fprintf(&b, " Its leftovers moved with it: %s.", strings.Join(parts, ", "))
	}
	if len(res.StrandedMeals) > 0 {
		var parts []string
		for _, m := range res.StrandedMeals {
			parts = append(parts, fmt.Sprintf("%q on %s %s", m.Title, weekdayOf(m.Day), m.Slot))
		}
		fmt.Fprintf(&b, " Note: %s could not follow and is now eaten before the meal it is leftovers of is cooked - move it later or replace it.",
			strings.Join(parts, ", "))
	}
	return b.String()
}

// markMealOnHand adds a meal's ingredients to the plan's on-hand list, which
// is what the shopping list reads to mark a line "already have" and leave it
// unpriced (pricing.ApplyOnHand) - and what makes that survive every rebuild
// of the list, where ticking the lines themselves would not outlive the
// reprice that puts this meal's lines there in the first place.
//
// On-hand is by food, not by amount: if another meal that week uses the same
// ingredient, its share is covered too. Returns how many names were added.
func markMealOnHand(ctx context.Context, s *Session, planID, mealID int64) (int, error) {
	ings, err := s.Store.ListIngredientsByMeal(ctx, mealID)
	if err != nil {
		return 0, err
	}
	names, err := s.Store.GetPlanOnHand(ctx, planID)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[strings.ToLower(strings.TrimSpace(n))] = true
	}
	added := 0
	for _, ing := range ings {
		key := strings.ToLower(strings.TrimSpace(ing.Name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, ing.Name)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	return added, s.Store.SetPlanOnHand(ctx, planID, names)
}

// onHandNote is the sentence a fill reports its on_hand marking with.
func onHandNote(ctx context.Context, s *Session, planID, mealID int64) string {
	if _, err := markMealOnHand(ctx, s, planID, mealID); err != nil {
		return " Note: could not mark its ingredients as already owned (" + err.Error() + ") - they are on the list to buy."
	}
	return " Its ingredients are on the shopping list marked as already owned, not priced."
}

// movedDays lists every day a move changed, for Session.planChanged.
func movedDays(res *plan.MoveResult, to string) []string {
	days := []string{res.FromDay, to}
	for _, m := range res.Followed {
		days = append(days, m.Day)
	}
	return days
}

// planMealView is one meal as the model sees it.
type planMealView struct {
	ID         int64  `json:"id"`
	Day        string `json:"day"`
	Weekday    string `json:"weekday"`
	Slot       string `json:"slot"`
	Title      string `json:"title"`
	Servings   int    `json:"servings"`
	IsLeftover bool   `json:"is_leftover,omitempty"`
	Locked     bool   `json:"locked,omitempty"`
}

func weekdayOf(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return strings.ToLower(t.Weekday().String())
}

// RegisterPlanTools adds every plan-shaped verb to a registry.
func RegisterPlanTools(r *Registry) {
	r.Register(&Tool{
		Name:        "read_plan",
		Description: "Read the whole current week's plan: every meal, its day, slot, servings, and whether it is leftovers. Start here when you need to know what is planned.",
		Run: func(ctx context.Context, s *Session, _ json.RawMessage) (Result, error) {
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			meals, err := s.Store.ListMealsByPlan(ctx, p.ID)
			if err != nil {
				return Result{}, err
			}
			days, _ := s.Store.ListPlanDays(ctx, p.ID)
			statusByDate := map[string]string{}
			for _, d := range days {
				if d.Status != "" && d.Status != db.DayCooking {
					statusByDate[d.Date] = d.Status
				}
			}

			views := make([]planMealView, 0, len(meals))
			for _, m := range meals {
				views = append(views, planMealView{
					ID: m.ID, Day: m.Day, Weekday: weekdayOf(m.Day), Slot: m.Slot,
					Title: m.Title, Servings: m.Servings,
					IsLeftover: m.IsLeftover, Locked: m.Locked,
				})
			}
			return Result{
				Summary: fmt.Sprintf("Read the plan for %s to %s (%d meals).", p.WeekStart, p.WeekEnd, len(views)),
				Data: map[string]any{
					"week_start":       p.WeekStart,
					"week_end":         p.WeekEnd,
					"meals":            views,
					"non_cooking_days": statusByDate,
				},
			}, nil
		},
	})

	r.Register(&Tool{
		Name:        "read_meal",
		Description: "Read one meal in full, including its ingredients and steps.",
		Params: map[string]Param{
			"meal_id": {Type: "integer", Description: "The meal's id, from read_plan."},
			"title":   {Type: "string", Description: "The meal's title, if you do not have its id. A unique part of the title is enough."},
		},
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				MealID int64  `json:"meal_id"`
				Title  string `json:"title"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			m, err := findMeal(ctx, s, p.ID, a.MealID, a.Title)
			if err != nil {
				return Result{}, err
			}
			ings, _ := s.Store.ListIngredientsByMeal(ctx, m.ID)
			lines := make([]map[string]any, 0, len(ings))
			for _, ing := range ings {
				lines = append(lines, map[string]any{
					"name": ing.Name, "quantity": ing.Quantity, "unit": ing.Unit,
				})
			}
			var steps []string
			if mr, _ := s.Store.GetMealRecipe(ctx, m.ID); mr != nil {
				_ = json.Unmarshal([]byte(mr.StepsJSON), &steps)
			}
			return Result{
				Summary: fmt.Sprintf("Read %q (%s %s).", m.Title, weekdayOf(m.Day), m.Slot),
				Data: map[string]any{
					"id": m.ID, "title": m.Title, "day": m.Day, "weekday": weekdayOf(m.Day),
					"slot": m.Slot, "servings": m.Servings, "effort": m.Effort,
					"is_leftover": m.IsLeftover, "ingredients": lines, "steps": steps,
				},
			}, nil
		},
	})

	r.Register(&Tool{
		Name:        "move_meal",
		Description: "Move a meal to a different day and/or slot. Anything already there is swapped into the vacated place, never deleted. Meals that eat its leftovers move the same number of days with it, so do not move those separately - the result lists where they went.",
		Params: map[string]Param{
			"meal_id": {Type: "integer", Description: "The meal's id."},
			"title":   {Type: "string", Description: "The meal's title, if you do not have its id."},
			"day":     {Type: "string", Description: "Destination day: a weekday name (monday) or YYYY-MM-DD inside the plan week."},
			"slot":    {Type: "string", Description: "Destination slot. Leave out to keep the meal in the slot it already occupies.", Enum: slotNames},
		},
		Required: []string{"day"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				MealID int64  `json:"meal_id"`
				Title  string `json:"title"`
				Day    string `json:"day"`
				Slot   string `json:"slot"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			m, err := findMeal(ctx, s, p.ID, a.MealID, a.Title)
			if err != nil {
				return Result{}, err
			}
			date, err := resolveDay(p, a.Day)
			if err != nil {
				return Result{}, err
			}
			slot := strings.ToLower(strings.TrimSpace(a.Slot))
			if slot == "" {
				slot = m.Slot
			} else if !validSlotName(slot) {
				return Result{}, fmt.Errorf("%q is not a meal slot - use breakfast, lunch, or dinner", a.Slot)
			}

			res, err := plan.MoveMeal(ctx, s.Store, plan.MoveParams{
				MealID: m.ID, Date: date, Slot: slot, Leftovers: plan.MoveFollow,
			})
			if err != nil {
				return Result{}, err
			}
			s.planChanged(movedDays(res, date)...)
			summary := fmt.Sprintf("Moved %q to %s %s.", m.Title, weekdayOf(date), slot)
			if res.Displaced != "" {
				summary += fmt.Sprintf(" %q took its old place on %s %s.", res.Displaced, weekdayOf(m.Day), m.Slot)
			}
			return Result{Summary: summary + moveSummary(res)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "swap_meals",
		Description: "Exchange the places of two meals that are both already on the plan. Each one's leftovers move with it. Only for trading two existing meals: when the user wants a slot to become leftovers of another meal (\"swap saturday lunch with leftover tacos\"), that is use_leftovers, and the meal being reheated stays where it is.",
		Params: map[string]Param{
			"first":  {Type: "string", Description: "Title of the first meal."},
			"second": {Type: "string", Description: "Title of the second meal."},
		},
		Required: []string{"first", "second"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				First  string `json:"first"`
				Second string `json:"second"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			m1, err := findMeal(ctx, s, p.ID, 0, a.First)
			if err != nil {
				return Result{}, err
			}
			m2, err := findMeal(ctx, s, p.ID, 0, a.Second)
			if err != nil {
				return Result{}, err
			}
			if m1.ID == m2.ID {
				return Result{}, fmt.Errorf("those are the same meal")
			}
			// MoveMeal already swaps whatever occupies the destination, so
			// moving one onto the other is the whole operation.
			res, err := plan.MoveMeal(ctx, s.Store, plan.MoveParams{
				MealID: m1.ID, Date: m2.Day, Slot: m2.Slot, Leftovers: plan.MoveFollow,
			})
			if err != nil {
				return Result{}, err
			}
			s.planChanged(movedDays(res, m2.Day)...)
			// Where each one landed, spelled out: left to work it out from two
			// titles, the model reports the places the wrong way round.
			return Result{Summary: fmt.Sprintf("Swapped them: %q is now on %s %s and %q is on %s %s.",
				m1.Title, weekdayOf(m2.Day), m2.Slot, m2.Title, weekdayOf(m1.Day), m1.Slot) + moveSummary(res)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "use_leftovers",
		Description: "Make a slot the leftovers of a meal cooked earlier in the week. Whatever is in the slot is replaced, the earlier meal stays where it is and cooks enough extra to cover it, and nothing new goes on the shopping list. Use this whenever the user asks for leftovers of a dish that is on the plan.",
		Params: map[string]Param{
			"source_meal_id": {Type: "integer", Description: "Id of the meal being reheated, from read_plan."},
			"source_title":   {Type: "string", Description: `Title of the meal being reheated, if you do not have its id - "Seasoned Chicken Tacos", not "Leftover Seasoned Chicken Tacos".`},
			"day":            {Type: "string", Description: "Day the leftovers are eaten: a weekday name or YYYY-MM-DD inside the plan week."},
			"slot":           {Type: "string", Description: "Which meal of that day.", Enum: slotNames},
		},
		Required: []string{"day", "slot"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				SourceMealID int64  `json:"source_meal_id"`
				SourceTitle  string `json:"source_title"`
				Day          string `json:"day"`
				Slot         string `json:"slot"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			date, err := resolveDay(p, a.Day)
			if err != nil {
				return Result{}, err
			}
			slot := strings.ToLower(strings.TrimSpace(a.Slot))
			if !validSlotName(slot) {
				return Result{}, fmt.Errorf("%q is not a meal slot", a.Slot)
			}

			meals, err := s.Store.ListMealsByPlan(ctx, p.ID)
			if err != nil {
				return Result{}, err
			}
			for _, m := range meals {
				if m.Day == date && m.Slot == slot && m.Locked {
					return Result{}, fmt.Errorf("the meal in %s %s is locked - the user has to unlock it before it can be replaced", weekdayOf(date), slot)
				}
			}
			// What can still be reheated then, nearest first. Named in every
			// error below so the model can correct itself or offer the choice.
			sources := plan.LeftoverSources(meals, date, slot)
			var options []string
			for _, m := range sources {
				options = append(options, fmt.Sprintf("%q from %s %s", m.Title, weekdayOf(m.Day), m.Slot))
			}
			available := "nothing cooked in the three days before it is available"
			if len(options) > 0 {
				available = "available: " + strings.Join(options, ", ")
			}

			// The source is named the way the user said it, "leftover" and all.
			src, err := findLeftoverSource(sources, a.SourceMealID, a.SourceTitle)
			if err != nil {
				return Result{}, fmt.Errorf("%v for %s %s - %s", err, weekdayOf(date), slot, available)
			}

			portions := float64(s.Household.HouseholdSize)
			if day, _ := s.Store.GetPlanDay(ctx, p.ID, date); day != nil && day.Portions > 0 {
				portions = day.Portions
			}
			res, err := plan.AddLeftoverMeal(ctx, s.Store, plan.LeftoverParams{
				PlanID: p.ID, SourceMealID: src.ID, Date: date, Slot: slot, Portions: portions,
			})
			if err != nil {
				return Result{}, err
			}
			// A day being filled is a day being eaten at home (handleMealFill).
			_ = s.Store.SetPlanDayStatus(ctx, p.ID, date, db.DayCooking)
			// No days named: the leftover is already sized to its day, and the
			// source keeps the extra it was just told to cook.
			s.planChanged()

			summary := fmt.Sprintf("Put %q on %s %s for %d", res.Title, weekdayOf(date), slot, res.Servings)
			for _, m := range meals {
				if m.Day == date && m.Slot == slot {
					summary += fmt.Sprintf(", replacing %q", m.Title)
				}
			}
			if res.ExtraCooked > 0 {
				summary += fmt.Sprintf(". %q on %s %s stays put and now cooks %d more to cover it.",
					res.SourceTitle, weekdayOf(src.Day), src.Slot, res.ExtraCooked)
			} else {
				summary += fmt.Sprintf(". %q on %s %s stays put and already cooks enough to cover it.",
					res.SourceTitle, weekdayOf(src.Day), src.Slot)
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "edit_meal",
		Description: "Rename a meal or change its effort or servings. This keeps the meal's ingredients and steps, so it cannot turn one dish into another - for that use fill_slot or create_meal. To change what day it is on, use move_meal.",
		Params: map[string]Param{
			"meal_id":   {Type: "integer", Description: "The meal's id."},
			"title":     {Type: "string", Description: "Current title, if you do not have the id."},
			"new_title": {Type: "string", Description: "New title. Leave out to keep it."},
			"effort":    {Type: "string", Description: "New effort level.", Enum: []string{"quick", "standard", "elaborate"}},
			"servings":  {Type: "integer", Description: "New serving count. Leave out to keep it."},
		},
		Mutates: true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				MealID   int64  `json:"meal_id"`
				Title    string `json:"title"`
				NewTitle string `json:"new_title"`
				Effort   string `json:"effort"`
				Servings int    `json:"servings"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			m, err := findMeal(ctx, s, p.ID, a.MealID, a.Title)
			if err != nil {
				return Result{}, err
			}

			// Unspecified fields keep their current value rather than being
			// zeroed - a rename must not silently reset the servings.
			title, effort, servings := m.Title, m.Effort, m.Servings
			var changed []string
			if t := strings.TrimSpace(a.NewTitle); t != "" && t != title {
				title = t
				changed = append(changed, "title")
			}
			if e := strings.ToLower(strings.TrimSpace(a.Effort)); e != "" && e != effort {
				if e != "quick" && e != "standard" && e != "elaborate" {
					return Result{}, fmt.Errorf("%q is not an effort level", a.Effort)
				}
				effort = e
				changed = append(changed, "effort")
			}
			if a.Servings > 0 && a.Servings != servings {
				servings = a.Servings
				changed = append(changed, "servings")
			}
			if len(changed) == 0 {
				return Result{Summary: fmt.Sprintf("%q already looks like that - nothing changed.", m.Title)}, nil
			}

			cooked := m.CookedPortions
			if cooked < servings {
				cooked = servings
			}
			if err := s.Store.UpdateMealTitle(ctx, m.ID, title, effort, servings, cooked); err != nil {
				return Result{}, err
			}
			s.planChanged()
			return Result{Summary: fmt.Sprintf("Updated %s on %q.", strings.Join(changed, " and "), m.Title)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "delete_meal",
		Description: "Remove a meal from the plan, leaving its slot empty.",
		Params: map[string]Param{
			"meal_id": {Type: "integer", Description: "The meal's id."},
			"title":   {Type: "string", Description: "The meal's title, if you do not have its id."},
		},
		Mutates: true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				MealID int64  `json:"meal_id"`
				Title  string `json:"title"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			m, err := findMeal(ctx, s, p.ID, a.MealID, a.Title)
			if err != nil {
				return Result{}, err
			}
			if err := s.Store.DeleteMeal(ctx, m.ID); err != nil {
				return Result{}, err
			}
			s.planChanged(m.Day)
			return Result{Summary: fmt.Sprintf("Removed %q from %s %s.", m.Title, weekdayOf(m.Day), m.Slot)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "fill_slot",
		Description: "Put one of the household's saved recipes into a slot, scaled to what that day is feeding. Whatever meal is already in the slot is replaced. Use search_recipes first to find the recipe id; if nothing fits, use create_meal instead.",
		Params: map[string]Param{
			"recipe_id": {Type: "integer", Description: "A saved recipe's id, from search_recipes."},
			"on_hand":   {Type: "boolean", Description: "true when the user says they already have what this dish needs. Its ingredients still go on the shopping list, marked as already owned and not priced."},
			"day":       {Type: "string", Description: "Weekday name or YYYY-MM-DD inside the plan week."},
			"slot":      {Type: "string", Description: "Which meal of the day.", Enum: slotNames},
		},
		Required: []string{"recipe_id", "day", "slot"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				RecipeID int64  `json:"recipe_id"`
				Day      string `json:"day"`
				Slot     string `json:"slot"`
				OnHand   bool   `json:"on_hand"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			date, err := resolveDay(p, a.Day)
			if err != nil {
				return Result{}, err
			}
			slot := strings.ToLower(strings.TrimSpace(a.Slot))
			if !validSlotName(slot) {
				return Result{}, fmt.Errorf("%q is not a meal slot", a.Slot)
			}

			portions := float64(s.Household.HouseholdSize)
			if day, _ := s.Store.GetPlanDay(ctx, p.ID, date); day != nil && day.Portions > 0 {
				portions = day.Portions
			}
			res, err := plan.MaterializeRecipe(ctx, s.Store, plan.MaterializeParams{
				PlanID: p.ID, HouseholdID: s.HouseholdID, CatalogRecipeID: a.RecipeID,
				Date: date, Slot: slot, Portions: portions,
			})
			if err != nil {
				return Result{}, err
			}
			s.planChanged(date)
			summary := fmt.Sprintf("Put %q on %s %s, scaled to %d servings.", res.Title, weekdayOf(date), slot, res.Servings)
			if len(res.Unquantified) > 0 {
				summary += fmt.Sprintf(" %s had no amount in the recipe.", strings.Join(res.Unquantified, ", "))
			}
			if a.OnHand {
				summary += onHandNote(ctx, s, p.ID, res.MealID)
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "create_meal",
		Description: "Put a meal that is not in the saved recipes into a slot, writing the recipe yourself: a title, the ingredients with amounts, and the steps. Whatever meal is already in the slot is replaced, and the recipe is saved for next time. Use this whenever the user names a dish for a slot and search_recipes has nothing that fits - never leave the old meal in place or the slot empty because no recipe was saved.",
		Params: map[string]Param{
			"day":          {Type: "string", Description: "Weekday name or YYYY-MM-DD inside the plan week."},
			"slot":         {Type: "string", Description: "Which meal of the day.", Enum: slotNames},
			"title":        {Type: "string", Description: `The dish's plain name, e.g. "Turkey Sandwich".`},
			"ingredients":  {Type: "array", Description: `Everything the dish needs, each as {"name": "sliced turkey", "quantity": 6, "unit": "oz"}. quantity is a number; unit is g, oz, lb, ml, cup, tbsp, tsp, or "each" for things counted whole.`},
			"steps":        {Type: "array", Description: "How to make it, as short strings in order."},
			"servings":     {Type: "integer", Description: "How many the ingredient amounts above feed. They are scaled from this to what the day needs. Defaults to the household size."},
			"effort":       {Type: "string", Description: "How much work it is.", Enum: []string{"quick", "standard", "elaborate"}},
			"prep_minutes": {Type: "integer", Description: "Hands-on time, if you know it."},
			"cook_minutes": {Type: "integer", Description: "Cooking time, if you know it."},
			"on_hand":      {Type: "boolean", Description: "true when the user says they already have what this dish needs. Its ingredients still go on the shopping list, marked as already owned and not priced."},
		},
		Required: []string{"day", "slot", "title", "ingredients"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Day         string `json:"day"`
				Slot        string `json:"slot"`
				Title       string `json:"title"`
				Ingredients []struct {
					Name string `json:"name"`
					// A number, or - from a model that quotes everything -
					// text like "2" or "1/2".
					Quantity json.RawMessage `json:"quantity"`
					Unit     string          `json:"unit"`
				} `json:"ingredients"`
				Steps       []string `json:"steps"`
				Servings    int      `json:"servings"`
				Effort      string   `json:"effort"`
				PrepMinutes int      `json:"prep_minutes"`
				CookMinutes int      `json:"cook_minutes"`
				OnHand      bool     `json:"on_hand"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, fmt.Errorf("create_meal arguments are the wrong shape (%v) - ingredients is a list of {name, quantity, unit}, steps a list of strings", err)
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			date, err := resolveDay(p, a.Day)
			if err != nil {
				return Result{}, err
			}
			slot := strings.ToLower(strings.TrimSpace(a.Slot))
			if !validSlotName(slot) {
				return Result{}, fmt.Errorf("%q is not a meal slot", a.Slot)
			}

			gm := plan.GeneratedMeal{
				Title: a.Title, Steps: a.Steps, Servings: a.Servings,
				PrepMinutes: a.PrepMinutes, CookMinutes: a.CookMinutes,
			}
			if gm.Servings < 1 {
				gm.Servings = s.Household.HouseholdSize
			}
			for _, ing := range a.Ingredients {
				name := strings.TrimSpace(ing.Name)
				if name == "" {
					continue
				}
				var qty float64
				if err := json.Unmarshal(ing.Quantity, &qty); err != nil {
					var text string
					_ = json.Unmarshal(ing.Quantity, &text)
					qty, _ = plan.ParseQuantity(text)
				}
				// A countable thing with no unit ("2 tortillas") is the usual
				// omission, and not worth a round trip to have it restated.
				unit := strings.TrimSpace(ing.Unit)
				if unit == "" {
					unit = "each"
				}
				gm.Ingredients = append(gm.Ingredients, plan.GeneratedIngredient{Name: name, Quantity: qty, Unit: unit})
			}

			portions := float64(s.Household.HouseholdSize)
			if day, _ := s.Store.GetPlanDay(ctx, p.ID, date); day != nil && day.Portions > 0 {
				portions = day.Portions
			}
			res, err := plan.WriteMeal(ctx, s.Store, plan.WriteMealParams{
				PlanID: p.ID, HouseholdID: s.HouseholdID,
				Date: date, Slot: slot, Portions: portions, Meal: gm,
			})
			if errors.Is(err, plan.ErrMealLocked) {
				return Result{}, fmt.Errorf("the meal in %s %s is locked - the user has to unlock it before it can be replaced", weekdayOf(date), slot)
			}
			if err != nil {
				return Result{}, err
			}
			// The effort the model gave beats the one guessed from its times.
			if e := strings.ToLower(strings.TrimSpace(a.Effort)); e == "quick" || e == "standard" || e == "elaborate" {
				if m, _ := s.Store.GetMealByID(ctx, res.MealID); m != nil && m.Effort != e {
					_ = s.Store.UpdateMealTitle(ctx, m.ID, m.Title, e, m.Servings, m.CookedPortions)
				}
			}
			s.planChanged(date)

			summary := fmt.Sprintf("Put %q on %s %s, %d servings", res.Title, weekdayOf(date), slot, res.Servings)
			if res.Replaced != "" {
				summary += fmt.Sprintf(", replacing %q", res.Replaced)
			}
			if res.Reused {
				summary += ". The household already had a recipe by that name, so that one was used."
			} else {
				summary += ", and saved it to the household's recipes."
			}
			if len(res.Stranded) > 0 {
				var parts []string
				for _, m := range res.Stranded {
					parts = append(parts, fmt.Sprintf("%q on %s %s", m.Title, weekdayOf(m.Day), m.Slot))
				}
				summary += fmt.Sprintf(" Note: %s was the replaced meal's leftovers and now has nothing to reheat - replace it or tell the user.",
					strings.Join(parts, ", "))
			}
			if a.OnHand {
				summary += onHandNote(ctx, s, p.ID, res.MealID)
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "set_day_status",
		Description: "Mark a whole day - every meal on it - as cooking, eating out, or skipped. A day that is not being cooked drops out of the shopping list. If the user is cooking something different for one meal, that is not a skipped day: put the new dish in that slot with fill_slot or create_meal.",
		Params: map[string]Param{
			"day":    {Type: "string", Description: "Weekday name or YYYY-MM-DD inside the plan week."},
			"status": {Type: "string", Description: "What is happening that day.", Enum: []string{db.DayCooking, db.DayEatingOut, db.DaySkipped}},
		},
		Required: []string{"day", "status"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Day    string `json:"day"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			date, err := resolveDay(p, a.Day)
			if err != nil {
				return Result{}, err
			}
			status := strings.ToLower(strings.TrimSpace(a.Status))
			if !db.ValidDayStatus(status) {
				return Result{}, fmt.Errorf("%q is not a day status - use cooking, eating_out, or skipped", a.Status)
			}

			// Say what it costs. Marking a day can strand a later one that was
			// eating its leftovers, and the assistant should report that rather
			// than let the user find an empty plate on Wednesday.
			var stranded []string
			if status != db.DayCooking {
				orphans, _ := s.Store.ListLeftoversSourcedFrom(ctx, p.ID, date)
				for _, o := range orphans {
					stranded = append(stranded, fmt.Sprintf("%q on %s %s", o.Title, weekdayOf(o.Day), o.Slot))
				}
			}
			if err := s.Store.SetPlanDayStatus(ctx, p.ID, date, status); err != nil {
				return Result{}, err
			}
			s.planChanged()
			summary := fmt.Sprintf("Marked %s as %s.", weekdayOf(date), strings.ReplaceAll(status, "_", " "))
			if len(stranded) > 0 {
				summary += fmt.Sprintf(" Note: %s was living off that day's leftovers and now has nothing.",
					strings.Join(stranded, ", "))
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "set_day_headcount",
		Description: "Set how many people are eating on a day. Rescales that day's recipes and every ingredient quantity, then the shopping list.",
		Params: map[string]Param{
			"day":       {Type: "string", Description: "Weekday name or YYYY-MM-DD inside the plan week."},
			"headcount": {Type: "integer", Description: "How many people are eating."},
		},
		Required: []string{"day", "headcount"},
		Mutates:  true,
		Run: func(ctx context.Context, s *Session, args json.RawMessage) (Result, error) {
			var a struct {
				Day       string `json:"day"`
				Headcount int    `json:"headcount"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return Result{}, err
			}
			if a.Headcount < 1 {
				return Result{}, fmt.Errorf("headcount must be at least 1")
			}
			p, err := currentPlan(ctx, s)
			if err != nil {
				return Result{}, err
			}
			date, err := resolveDay(p, a.Day)
			if err != nil {
				return Result{}, err
			}
			if err := s.Store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
				PlanID: p.ID, Date: date, Headcount: a.Headcount,
			}); err != nil {
				return Result{}, err
			}
			scaled, err := s.Store.ScaleMealsForDay(ctx, p.ID, date, float64(a.Headcount))
			if err != nil {
				return Result{}, err
			}
			s.planChanged()
			return Result{Summary: fmt.Sprintf("%s now serves %d - rescaled %d meals.",
				weekdayOf(date), a.Headcount, scaled.MealsScaled)}, nil
		},
	})
}
