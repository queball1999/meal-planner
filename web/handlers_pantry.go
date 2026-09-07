package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"goeat/catalog"
	"goeat/db"
	"goeat/middleware"
	"goeat/pricing"
)

type pantryPageData struct {
	Items    []*db.PantryItem
	FilterQ  string
	Filtered bool
	Page     Pagination
}

func (s *Server) handlePantryPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	items, _ := s.store.FilterPantryItems(r.Context(), hh.ID, q)
	items, page := paginate(r, items)
	s.render(w, r, "pantry", pantryPageData{Items: items, FilterQ: q, Filtered: q != "", Page: page})
}

func (s *Server) handlePantryAdd(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	rawName := strings.TrimSpace(r.FormValue("name"))
	if rawName == "" {
		s.setNotify(w, NotifyDanger, "Ingredient name is required.")
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
	pi, err := s.store.CreatePantryItem(r.Context(), db.CreatePantryItemParams{
		HouseholdID:    hh.ID,
		Name:           rawName,
		NormalizedTerm: normalized,
		QuantityOnHand: qty,
		Unit:           unit,
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Error saving pantry item: %v", err))
	} else {
		linkPantryItem(r, s.store, hh.ID, pi)
		s.setNotify(w, NotifySuccess, fmt.Sprintf("%q added to pantry.", rawName))
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
	pi, _ := s.store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID:    hh.ID,
		Name:           target.DisplayName,
		NormalizedTerm: normalized,
		QuantityOnHand: target.BuyQuantity * float64(target.PackSize),
		Unit:           target.PurchaseUnit,
	})
	linkPantryItem(r, s.store, hh.ID, pi)

	w.WriteHeader(http.StatusNoContent)
}

// linkPantryItem ensures a catalog item for a freshly upserted pantry row and
// records the item_id. Best-effort: catalog linkage never blocks a pantry save.
func linkPantryItem(r *http.Request, store db.Store, householdID int64, pi *db.PantryItem) {
	if pi == nil {
		return
	}
	it, err := catalog.EnsureItem(r.Context(), store, householdID, pi.Name)
	if err != nil || it == nil {
		return
	}
	id := it.ID
	_ = store.SetPantryItemItem(r.Context(), pi.ID, &id)
}
