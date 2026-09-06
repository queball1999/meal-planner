package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"goeat/db"
	"goeat/middleware"
	"goeat/pricing"
)

type pantryPageData struct {
	Items []*db.PantryItem
}

func (s *Server) handlePantryPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	items, _ := s.store.ListPantryItems(r.Context(), hh.ID)
	s.render(w, r, "pantry", pantryPageData{Items: items})
}

func (s *Server) handlePantryAdd(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	rawName := strings.TrimSpace(r.FormValue("name"))
	if rawName == "" {
		s.setFlash(w, "Ingredient name is required.")
		http.Redirect(w, r, "/pantry", http.StatusSeeOther)
		return
	}

	qtyStr := strings.TrimSpace(r.FormValue("quantity"))
	qty, _ := strconv.ParseFloat(qtyStr, 64)
	if qty <= 0 {
		qty = 1
	}
	unit := strings.TrimSpace(r.FormValue("unit"))
	if unit == "" {
		unit = "each"
	}

	normalized := pricing.Normalize(rawName)
	_, err := s.store.CreatePantryItem(r.Context(), db.CreatePantryItemParams{
		HouseholdID:    hh.ID,
		Name:           rawName,
		NormalizedTerm: normalized,
		QuantityOnHand: qty,
		Unit:           unit,
	})
	if err != nil {
		s.setFlash(w, fmt.Sprintf("Error saving pantry item: %v", err))
	} else {
		s.setFlash(w, fmt.Sprintf("%q added to pantry.", rawName))
	}
	http.Redirect(w, r, "/pantry", http.StatusSeeOther)
}

func (s *Server) handlePantryDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	_ = s.store.DeletePantryItem(r.Context(), id)
	http.Redirect(w, r, "/pantry", http.StatusSeeOther)
}

// handlePantryStock is called from the shopping list when a user wants to move
// a checked item into the pantry. It marks the shopping list item in_pantry and
// upserts a pantry_item with the bought quantity.
func (s *Server) handlePantryStock(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "no household", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	// Load shopping list item to get display name + quantity.
	plan, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if plan == nil {
		http.Error(w, "no plan", http.StatusNotFound)
		return
	}
	items, _ := s.store.ListShoppingListItems(ctx, plan.ID)
	var target *db.ShoppingListItem
	for _, item := range items {
		if item.ID == id {
			target = item
			break
		}
	}
	if target == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	_ = s.store.MarkShoppingListItemInPantry(ctx, id, true)

	normalized := pricing.Normalize(target.DisplayName)
	_, _ = s.store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID:    hh.ID,
		Name:           target.DisplayName,
		NormalizedTerm: normalized,
		QuantityOnHand: target.BuyQuantity * float64(target.PackSize),
		Unit:           target.PurchaseUnit,
	})

	w.WriteHeader(http.StatusNoContent)
}
