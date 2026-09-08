package web

import (
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
