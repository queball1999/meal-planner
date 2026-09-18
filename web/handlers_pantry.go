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

// pantryRow adds the catalog item's photo to a pantry row for display - a
// pantry list of dozens of ingredients reads a lot faster with a thumbnail per
// row than with names alone, matching the catalog and recipe lists.
type pantryRow struct {
	*db.PantryItem
	ImageURL string
}

type pantryPageData struct {
	Items    []pantryRow
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
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	items, _ := s.store.FilterPantryItems(ctx, hh.ID, q)
	items, page := paginate(r, items)

	// One catalog read for the whole page rather than a lookup per row.
	catalogItems, _ := s.store.ListItems(ctx, hh.ID)
	itemsByID := make(map[int64]*db.Item, len(catalogItems))
	for _, it := range catalogItems {
		itemsByID[it.ID] = it
	}
	rows := make([]pantryRow, len(items))
	for i, pi := range items {
		var linked *db.Item
		if pi.ItemID != nil {
			linked = itemsByID[*pi.ItemID]
		}
		// Viewing the pantry list is "viewing these items directly" - queue an
		// immediate fetch for whichever ones are missing a photo, same as the
		// items catalog page and an item's own detail page already do. Only
		// the page actually shown (post-pagination via `items` above), not
		// every pantry row in the household.
		s.lazyFetchItemImage(ctx, linked)
		rows[i] = pantryRow{PantryItem: pi, ImageURL: itemImageURL(linked)}
	}

	s.render(w, r, "pantry", pantryPageData{Items: rows, FilterQ: q, Filtered: q != "", Page: page})
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
		// BuyQuantity is already the total amount in purchase units (costing
		// stores packs x pack_size), so multiplying by the pack size again
		// stocked the pantry with several times what was bought.
		QuantityOnHand: target.BuyQuantity,
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
