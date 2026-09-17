package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"goeat/middleware"
)

// handleDangerWipe permanently deletes one category of household data
// (Settings → Danger zone). Unlike a plan regenerate's soft-cancel, nothing
// here is kept for history - each target is confirmed client-side before the
// request is even sent (see settings.html's onsubmit confirm()).
func (s *Server) handleDangerWipe(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	target := r.PathValue("target")

	var err error
	var successMsg string
	switch target {
	case "recipes":
		err = s.store.DeleteAllCatalogRecipesForHousehold(ctx, hh.ID)
		successMsg = "Every saved recipe was deleted."
	case "pantry":
		err = s.store.DeleteAllPantryItemsForHousehold(ctx, hh.ID)
		successMsg = "Pantry cleared."
	case "lists":
		err = s.store.DeleteAllShoppingListItemsForHousehold(ctx, hh.ID)
		successMsg = "Every shopping list was cleared."
	case "plans":
		err = s.store.DeleteAllPlansForHousehold(ctx, hh.ID)
		successMsg = "Every meal plan was deleted."
	case "all":
		err = s.wipeEverything(ctx, hh.ID)
		successMsg = "Everything was wiped: recipes, pantry, shopping lists, and meal plans."
	default:
		http.Error(w, "unknown wipe target", http.StatusBadRequest)
		return
	}

	var actorID *int64
	if u := middleware.UserFromCtx(r); u != nil {
		id := u.ID
		actorID = &id
	}

	if err != nil {
		s.logEvent(r, actorID, "danger.wipe.failed", "household", fmt.Sprintf("%d", hh.ID), target)
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Wipe failed: %v", err))
	} else {
		s.logEvent(r, actorID, "danger.wipe", "household", fmt.Sprintf("%d", hh.ID), target)
		s.setNotify(w, NotifySuccess, successMsg)
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// wipeEverything runs every individual danger-zone wipe for a household in
// one go ("Wipe everything"). Each one is independent (different tables,
// none depending on another having succeeded), so a failure in one does not
// stop the rest - the household ends up with as much wiped as possible
// rather than stopping at the first error, and every failure is reported
// rather than only the first.
func (s *Server) wipeEverything(ctx context.Context, householdID int64) error {
	var errs []error
	if err := s.store.DeleteAllCatalogRecipesForHousehold(ctx, householdID); err != nil {
		errs = append(errs, fmt.Errorf("recipes: %w", err))
	}
	if err := s.store.DeleteAllPantryItemsForHousehold(ctx, householdID); err != nil {
		errs = append(errs, fmt.Errorf("pantry: %w", err))
	}
	if err := s.store.DeleteAllShoppingListItemsForHousehold(ctx, householdID); err != nil {
		errs = append(errs, fmt.Errorf("shopping lists: %w", err))
	}
	if err := s.store.DeleteAllPlansForHousehold(ctx, householdID); err != nil {
		errs = append(errs, fmt.Errorf("plans: %w", err))
	}
	return errors.Join(errs...)
}
