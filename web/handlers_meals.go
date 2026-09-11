package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"goeat/db"
	"goeat/middleware"
)

type mealPageData struct {
	Meal        *db.Meal
	Recipe      *db.MealRecipe
	Steps       []string
	Ingredients []*db.MealIngredient
	SourceMeal  *db.Meal // non-nil when IsLeftover
	PlanID      int64
	WeekStart   string
	Feedback    int    // -1, 0, or 1 for current user's rating
	ImageURL    string // "" when no matching recipe photo - see handleMealCard's own note
	CatalogID   int64  // matching catalog recipe id, 0 if none - lets the page link/upload to it
}

func (s *Server) handleMealDetail(w http.ResponseWriter, r *http.Request) {
	mealID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad meal id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	meal, err := s.store.GetMealByID(ctx, mealID)
	if err != nil || meal == nil {
		http.NotFound(w, r)
		return
	}

	recipe, _ := s.store.GetMealRecipe(ctx, mealID)
	ingredients, _ := s.store.ListIngredientsByMeal(ctx, mealID)

	var steps []string
	if recipe != nil && recipe.StepsJSON != "" {
		_ = json.Unmarshal([]byte(recipe.StepsJSON), &steps)
	}

	var sourceMeal *db.Meal
	if meal.IsLeftover && meal.LeftoverSourceMealID != nil {
		sourceMeal, _ = s.store.GetMealByID(ctx, *meal.LeftoverSourceMealID)
	}

	plan, _ := s.store.GetPlanByID(ctx, meal.PlanID)
	weekStart := ""
	if plan != nil {
		weekStart = plan.WeekStart
	}

	// A meal has no image or catalog page of its own, but every generated meal
	// is saved to the recipe catalog under the same title, and that row can
	// have a photo and a "View recipe" destination. Same loose title match
	// handleMealCard uses for the hover preview.
	imageURL := ""
	var catalogID int64
	if hh := middleware.HouseholdFromCtx(r); hh != nil {
		if rc, _ := s.store.GetCatalogRecipeByTitle(ctx, hh.ID, meal.Title); rc != nil {
			catalogID = rc.ID
			if rc.ImagePath != "" {
				imageURL = "/recipe-images/" + rc.ImagePath
			}
		}
	}

	s.render(w, r, "meal", mealPageData{
		Meal:        meal,
		Recipe:      recipe,
		Steps:       steps,
		Ingredients: ingredients,
		SourceMeal:  sourceMeal,
		PlanID:      meal.PlanID,
		WeekStart:   weekStart,
		ImageURL:    imageURL,
		CatalogID:   catalogID,
	})
}

func (s *Server) handleMealFeedback(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	mealID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad meal id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	meal, err := s.store.GetMealByID(ctx, mealID)
	if err != nil || meal == nil {
		http.NotFound(w, r)
		return
	}

	ratingStr := r.FormValue("rating")
	rating, _ := strconv.Atoi(ratingStr)
	if rating != 1 && rating != -1 {
		http.Error(w, "rating must be 1 or -1", http.StatusBadRequest)
		return
	}

	mID := mealID
	_, _ = s.store.CreateFeedback(ctx, db.CreateFeedbackParams{
		HouseholdID: hh.ID,
		MealID:      &mID,
		Title:       meal.Title,
		Rating:      rating,
	})

	label := "Feedback saved."
	if rating == 1 {
		label = fmt.Sprintf("Liked %q - we'll include more meals like this.", meal.Title)
	} else {
		label = fmt.Sprintf("Disliked %q - we'll avoid similar meals.", meal.Title)
	}
	s.setNotify(w, NotifySuccess, label)
	http.Redirect(w, r, fmt.Sprintf("/meals/%d", mealID), http.StatusSeeOther)
}

func (s *Server) handleMealLock(w http.ResponseWriter, r *http.Request) {
	mealID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad meal id", http.StatusBadRequest)
		return
	}
	locked := r.FormValue("locked") == "1"
	_ = s.store.UpdateMealLocked(r.Context(), mealID, locked)
	http.Redirect(w, r, "/plan", http.StatusSeeOther)
}
