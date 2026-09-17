package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"goeat/auth"
	"goeat/middleware"
)

// accountPageData drives /account: the self-service page for the signed-in
// user - password, a full data export, and the two destructive resets that
// used to be scattered on the Settings danger panel.
type accountPageData struct {
	Username    string
	PlanCount   int
	RecipeCount int
	PantryCount int
}

// resetConfirmWord / wipeConfirmWord are what the user must type. Enforced
// server-side, not just in the page: both endpoints are reachable without it.
const (
	resetConfirmWord = "RESET"
	wipeConfirmWord  = "DELETE"
)

func (s *Server) handleAccountPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromCtx(r)
	hh := middleware.HouseholdFromCtx(r)
	if user == nil || hh == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	data := accountPageData{Username: user.Username}
	if plans, err := s.store.ListPlans(ctx, hh.ID); err == nil {
		data.PlanCount = len(plans)
	}
	if recipes, err := s.store.ListCatalogRecipes(ctx, hh.ID); err == nil {
		data.RecipeCount = len(excludeLeftoverRecipes(recipes))
	}
	if pantry, err := s.store.ListPantryItems(ctx, hh.ID); err == nil {
		data.PantryCount = len(pantry)
	}

	s.render(w, r, "account", data)
}

// handleAccountPassword changes the signed-in user's password (POST form:
// current, new, confirm).
func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromCtx(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Invalid form submission.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	fail := func(msg string) {
		s.setNotify(w, NotifyDanger, msg)
		http.Redirect(w, r, "/account", http.StatusSeeOther)
	}

	if auth.CheckPassword(user.PasswordHash, current) != nil {
		id := user.ID
		s.logEvent(r, &id, "account.password.failed", "user", fmt.Sprintf("%d", id), `{"reason":"wrong_current"}`)
		fail("Current password is incorrect.")
		return
	}
	if next != confirm {
		fail("The new password and its confirmation do not match.")
		return
	}
	if err := auth.ValidatePassword(next); err != nil {
		fail(err.Error())
		return
	}
	if auth.CheckPassword(user.PasswordHash, next) == nil {
		fail("The new password must be different from the current one.")
		return
	}

	hash, err := auth.HashPassword(next)
	if err != nil {
		fail("Could not process the new password. Try again.")
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), user.ID, hash); err != nil {
		fail("Could not save the new password. Try again.")
		return
	}

	// Every other session for this user is now stale - sign them all out so a
	// changed password actually locks out anyone who had one.
	_ = s.store.DeleteUserSessions(r.Context(), user.ID)
	id := user.ID
	s.logEvent(r, &id, "account.password.changed", "user", fmt.Sprintf("%d", id), "")
	s.clearSessionCookie(w)
	s.setNotify(w, NotifySuccess, "Password changed. Please sign in again.")
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// handleAccountExport streams a single JSON document of everything the
// household owns: preferences, members, stores, plans (with their meals and
// shopping lists), saved recipes, the pantry and the item catalog.
func (s *Server) handleAccountExport(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	type mealOut struct {
		Meal        any `json:"meal"`
		Ingredients any `json:"ingredients"`
	}
	type planOut struct {
		Plan         any       `json:"plan"`
		Meals        []mealOut `json:"meals"`
		ShoppingList any       `json:"shopping_list"`
	}
	type recipeOut struct {
		Recipe      any `json:"recipe"`
		Ingredients any `json:"ingredients"`
		Steps       any `json:"steps"`
	}

	out := map[string]any{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"app":         s.cfg.AppName,
	}
	out["household"], _ = s.store.GetHousehold(ctx)
	out["preferences"], _ = s.store.GetPreferences(ctx, hh.ID)
	out["members"], _ = s.store.ListHouseholdMembers(ctx, hh.ID)
	out["stores"], _ = s.store.ListStores(ctx, hh.ID)
	out["pantry"], _ = s.store.ListPantryItems(ctx, hh.ID)
	out["items"], _ = s.store.ListItems(ctx, hh.ID)

	plans, _ := s.store.ListPlans(ctx, hh.ID)
	planOuts := make([]planOut, 0, len(plans))
	for _, p := range plans {
		po := planOut{Plan: p}
		if meals, err := s.store.ListMealsByPlan(ctx, p.ID); err == nil {
			for _, m := range meals {
				ings, _ := s.store.ListIngredientsByMeal(ctx, m.ID)
				po.Meals = append(po.Meals, mealOut{Meal: m, Ingredients: ings})
			}
		}
		po.ShoppingList, _ = s.store.ListShoppingListItems(ctx, p.ID)
		planOuts = append(planOuts, po)
	}
	out["plans"] = planOuts

	recipes, _ := s.store.ListCatalogRecipes(ctx, hh.ID)
	recipeOuts := make([]recipeOut, 0, len(recipes))
	for _, rc := range recipes {
		ro := recipeOut{Recipe: rc}
		ro.Ingredients, _ = s.store.ListCatalogRecipeIngredients(ctx, rc.ID)
		ro.Steps, _ = s.store.ListCatalogRecipeSteps(ctx, rc.ID)
		recipeOuts = append(recipeOuts, ro)
	}
	out["recipes"] = recipeOuts

	fname := fmt.Sprintf("goeat-export-%s.json", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+fname)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		// Headers are already out; nothing to do but log.
		s.logEvent(r, nil, "account.export.error", "household", fmt.Sprintf("%d", hh.ID), "")
	}
}

// handleAccountReset clears the meal-planning data (plans + shopping lists) but
// keeps recipes, the pantry, preferences and members. Typed "RESET" required.
func (s *Server) handleAccountReset(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if !confirmWordMatches(r, resetConfirmWord) {
		s.setNotify(w, NotifyDanger, `Type RESET to confirm.`)
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	var firstErr error
	for _, step := range []func() error{
		func() error { return s.store.DeleteAllShoppingListItemsForHousehold(ctx, hh.ID) },
		func() error { return s.store.DeleteAllPlansForHousehold(ctx, hh.ID) },
	} {
		if err := step(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Reset failed: %v", firstErr))
	} else {
		s.logEvent(r, nil, "account.reset.plans", "household", fmt.Sprintf("%d", hh.ID), "")
		s.setNotify(w, NotifySuccess, "Meal plans and shopping lists cleared. Recipes and pantry kept.")
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// handleAccountWipe deletes every piece of household content - plans, lists,
// saved recipes, the pantry and the item catalog. Preferences, members and the
// account itself are kept so the app is usable again immediately. Typed
// "DELETE" required.
func (s *Server) handleAccountWipe(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if !confirmWordMatches(r, wipeConfirmWord) {
		s.setNotify(w, NotifyDanger, `Type DELETE to confirm.`)
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	var firstErr error
	for _, step := range []func() error{
		func() error { return s.store.DeleteAllShoppingListItemsForHousehold(ctx, hh.ID) },
		func() error { return s.store.DeleteAllPlansForHousehold(ctx, hh.ID) },
		func() error { return s.store.DeleteAllCatalogRecipesForHousehold(ctx, hh.ID) },
		func() error { return s.store.DeleteAllPantryItemsForHousehold(ctx, hh.ID) },
	} {
		if err := step(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Delete failed: %v", firstErr))
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.logEvent(r, nil, "account.wipe", "household", fmt.Sprintf("%d", hh.ID), "")
	s.setNotify(w, NotifySuccess, "All plans, recipes, shopping lists and pantry data were deleted.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// confirmWordMatches reports whether the form's "confirm" field is exactly the
// required word (case-sensitive, trimmed).
func confirmWordMatches(r *http.Request, want string) bool {
	if err := r.ParseForm(); err != nil {
		return false
	}
	return strings.TrimSpace(r.FormValue("confirm")) == want
}
