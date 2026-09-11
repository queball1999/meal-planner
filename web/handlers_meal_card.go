package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"goeat/db"
	"goeat/middleware"
)

// mealCardIngredient is one line on a hover card.
type mealCardIngredient struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// mealCard is the preview shown when hovering a meal.
type mealCard struct {
	OK          bool                 `json:"ok"`
	Title       string               `json:"title"`
	Slot        string               `json:"slot"`
	DayLabel    string               `json:"day_label"`
	Effort      string               `json:"effort"`
	Servings    int                  `json:"servings"`
	Cost        string               `json:"cost,omitempty"`
	ImageURL    string               `json:"image_url,omitempty"`
	IsLeftover  bool                 `json:"is_leftover"`
	Ingredients []mealCardIngredient `json:"ingredients"`
	More        int                  `json:"more,omitempty"`
	URL         string               `json:"url"`
}

// mealCardMaxIngredients caps the list. A hover card is a glance, not the meal
// page - a card that scrolls has stopped being a preview.
const mealCardMaxIngredients = 8

// handleMealCard returns the hover-card preview for one meal.
//
// JSON rather than an HTML fragment: the card is positioned and reused by one
// floating element (hovercard.js), so shipping markup would mean the server
// deciding layout for a component that already knows its own.
func (s *Server) handleMealCard(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	ctx := r.Context()

	meal, err := s.store.GetMealByID(ctx, id)
	if err != nil || meal == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
		return
	}
	// Scoped through the plan: a meal id belongs to a plan, and a plan to a
	// household, so this is what stops one household previewing another's.
	plan, _ := s.store.GetPlanByID(ctx, meal.PlanID)
	if plan == nil || plan.HouseholdID != hh.ID {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
		return
	}

	card := mealCard{
		OK:         true,
		Title:      meal.Title,
		Slot:       meal.Slot,
		DayLabel:   dayLabel(meal.Day),
		Effort:     meal.Effort,
		Servings:   meal.Servings,
		IsLeftover: meal.IsLeftover,
		URL:        fmt.Sprintf("/meals/%d", meal.ID),
	}

	ings, _ := s.store.ListIngredientsByMeal(ctx, meal.ID)
	for i, ing := range ings {
		if i >= mealCardMaxIngredients {
			card.More = len(ings) - mealCardMaxIngredients
			break
		}
		amount := ""
		if ing.Quantity > 0 {
			amount = qtyLabel(ing.Quantity, ing.Unit)
		}
		card.Ingredients = append(card.Ingredients, mealCardIngredient{Name: ing.Name, Amount: amount})
	}

	card.Cost = s.mealCostLabel(ctx, meal, ings)

	// A meal has no image of its own, but every generated meal is saved to the
	// recipe catalog under the same title (commit 10), and that row can have a
	// photo. Matching by title is loose, and deliberately so: a wrong photo on
	// a hover card costs nothing, and requiring a hard link would mean no
	// photos at all until every meal carried a recipe id.
	if rc, _ := s.store.GetCatalogRecipeByTitle(ctx, hh.ID, meal.Title); rc != nil && rc.ImagePath != "" {
		card.ImageURL = "/recipe-images/" + rc.ImagePath
	}

	writeJSON(w, http.StatusOK, card)
}

// mealCostLabel adds up what this meal's ingredients contribute to the
// shopping list.
//
// A shopping line is shared between every meal that uses the ingredient, so
// its full cost cannot simply be attributed here - two meals using the same
// onions would each claim the whole bag. The line is split evenly across the
// meals that reference it, which is an approximation, and the card says
// "about" rather than presenting it as exact.
func (s *Server) mealCostLabel(ctx context.Context, meal *db.Meal, ings []*db.MealIngredient) string {
	if len(ings) == 0 {
		return ""
	}
	lines, err := s.store.ListShoppingListItems(ctx, meal.PlanID)
	if err != nil || len(lines) == 0 {
		return ""
	}
	cents := mealCostCentsFromLines(lines, ings)
	if cents == 0 {
		return ""
	}
	return fmt.Sprintf("about $%.2f", float64(cents)/100)
}

// mealCostCentsFromLines is mealCostLabel's split-the-line math against an
// already-loaded set of shopping list lines - callers pricing every meal in a
// plan share one query instead of repeating it per meal.
func mealCostCentsFromLines(lines []*db.ShoppingListItem, ings []*db.MealIngredient) int64 {
	if len(ings) == 0 || len(lines) == 0 {
		return 0
	}

	mine := make(map[int64]bool, len(ings))
	for _, ing := range ings {
		mine[ing.ID] = true
	}

	var total int64
	for _, ln := range lines {
		if ln.InPantry || ln.LineTotalCents == 0 {
			continue
		}
		refs := shoppingLineRefs(ln.MealIngredientRefs)
		if len(refs) == 0 {
			continue
		}
		var hits int
		for _, ref := range refs {
			if mine[ref] {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		total += ln.LineTotalCents * int64(hits) / int64(len(refs))
	}
	return total
}

// shoppingLineRefs decodes a line's meal_ingredient_refs JSON array. A bad or
// empty value means the line cannot be attributed to any meal, which is a
// reason to skip it, not to fail the card.
func shoppingLineRefs(raw string) []int64 {
	if raw == "" {
		return nil
	}
	var refs []int64
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil
	}
	return refs
}
