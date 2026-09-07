package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"goeat/db"
	"goeat/plan"
)

var slotNames = []string{"breakfast", "lunch", "dinner"}

var weekdayOffset = map[string]int{
	"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3,
	"thursday": 4, "friday": 5, "saturday": 6,
}

// currentPlan loads the household's active plan, with a message the model can
// act on when there is none.
func currentPlan(ctx context.Context, s *Session) (*db.Plan, error) {
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
	if off, ok := weekdayOffset[d]; ok {
		start, err := time.Parse("2006-01-02", p.WeekStart)
		if err != nil {
			return "", fmt.Errorf("this plan has an unreadable week start (%q)", p.WeekStart)
		}
		return start.AddDate(0, 0, off).Format("2006-01-02"), nil
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
		Description: "Move a meal to a different day and/or slot. Anything already there is swapped into the vacated place, never deleted.",
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

			displaced, err := s.Store.MoveMeal(ctx, m.ID, date, slot)
			if err != nil {
				return Result{}, err
			}
			summary := fmt.Sprintf("Moved %q to %s %s.", m.Title, weekdayOf(date), slot)
			if displaced != "" {
				summary += fmt.Sprintf(" %q took its old place on %s %s.", displaced, weekdayOf(m.Day), m.Slot)
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "swap_meals",
		Description: "Swap two meals with each other.",
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
			if _, err := s.Store.MoveMeal(ctx, m1.ID, m2.Day, m2.Slot); err != nil {
				return Result{}, err
			}
			return Result{Summary: fmt.Sprintf("Swapped %q and %q.", m1.Title, m2.Title)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "edit_meal",
		Description: "Rename a meal or change its effort or servings. To change what day it is on, use move_meal.",
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
			return Result{Summary: fmt.Sprintf("Removed %q from %s %s.", m.Title, weekdayOf(m.Day), m.Slot)}, nil
		},
	})

	r.Register(&Tool{
		Name:        "fill_slot",
		Description: "Put one of the household's saved recipes into a slot, scaled to what that day is feeding. Use search_recipes first to find the recipe id.",
		Params: map[string]Param{
			"recipe_id": {Type: "integer", Description: "A saved recipe's id, from search_recipes."},
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
			summary := fmt.Sprintf("Put %q on %s %s, scaled to %d servings.", res.Title, weekdayOf(date), slot, res.Servings)
			if len(res.Unquantified) > 0 {
				summary += fmt.Sprintf(" %s had no amount in the recipe.", strings.Join(res.Unquantified, ", "))
			}
			return Result{Summary: summary}, nil
		},
	})

	r.Register(&Tool{
		Name:        "set_day_status",
		Description: "Mark a day as cooking, eating out, or skipped. A day that is not being cooked drops out of the shopping list.",
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
			return Result{Summary: fmt.Sprintf("%s now serves %d - rescaled %d meals.",
				weekdayOf(date), a.Headcount, scaled.MealsScaled)}, nil
		},
	})
}
