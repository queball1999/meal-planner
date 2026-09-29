package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"goeat/catalog"
	"goeat/db"
	"goeat/middleware"
)

type adminPricesPageData struct {
	Stores []storeWithPrices
	Items  []*db.Item // catalog items for the picker
}

type manualPriceRow struct {
	*db.ManualPrice
	PriceLabel  string
	DisplayName string // catalog item name when the term resolves, else the term
}

type storeWithPrices struct {
	Store  *db.GroceryStore
	Prices []manualPriceRow
	// Page scopes its parameter to this store (page_<id>), so paging one
	// store's prices leaves every other table on the page where it was.
	Page Pagination
}

func (s *Server) handleAdminPricesPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	stores, _ := s.store.ListStores(ctx, hh.ID)

	catItems, _ := s.store.ListItems(ctx, hh.ID)
	nameByTerm := make(map[string]string, len(catItems))
	for _, it := range catItems {
		nameByTerm[it.NormalizedTerm] = it.Name
	}

	var rows []storeWithPrices
	for _, gs := range stores {
		rawPrices, _ := s.store.ListManualPrices(ctx, gs.ID)
		var priceRows []manualPriceRow
		for _, p := range rawPrices {
			display := p.NormalizedTerm
			if n := nameByTerm[p.NormalizedTerm]; n != "" {
				display = n
			}
			priceRows = append(priceRows, manualPriceRow{
				ManualPrice: p,
				PriceLabel:  fmt.Sprintf("$%.2f", float64(p.PriceCents)/100),
				DisplayName: display,
			})
		}
		priceRows, page := paginateNamed(r, priceRows, fmt.Sprintf("page_%d", gs.ID))
		rows = append(rows, storeWithPrices{Store: gs, Prices: priceRows, Page: page})
	}
	s.render(w, r, "admin_prices", adminPricesPageData{Stores: rows, Items: catItems})
}

func (s *Server) handleAdminPriceCreate(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	storeID, err := strconv.ParseInt(r.FormValue("store_id"), 10, 64)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Invalid store.")
		http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
		return
	}
	if !s.owns(w, r, db.ResStore, storeID) {
		return
	}

	ctx := r.Context()

	// Prefer an explicit catalog item pick; fall back to a typed name, which
	// is turned into a catalog item so the price attaches to something real.
	var item *db.Item
	if idStr := strings.TrimSpace(r.FormValue("item_id")); idStr != "" {
		if itemID, perr := strconv.ParseInt(idStr, 10, 64); perr == nil {
			it, _ := s.store.GetItem(ctx, itemID)
			if it != nil && it.HouseholdID == hh.ID {
				item = it
			}
		}
	}
	rawName := strings.TrimSpace(r.FormValue("name"))
	if item == nil {
		if rawName == "" {
			s.setNotify(w, NotifyDanger, "Choose an item or enter a name.")
			http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
			return
		}
		item, _ = catalog.EnsureItem(ctx, s.store, hh.ID, rawName)
	}
	if item == nil {
		s.setNotify(w, NotifyDanger, "Could not resolve that item.")
		http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
		return
	}

	dollarStr := strings.TrimSpace(r.FormValue("price_dollars"))
	dollars, err := strconv.ParseFloat(dollarStr, 64)
	if err != nil || dollars <= 0 {
		s.setNotify(w, NotifyDanger, "Invalid price - enter a positive dollar amount.")
		http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
		return
	}

	packSize, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("pack_size")), 64)
	if packSize <= 0 {
		packSize = 1
	}
	purchaseUnit := strings.TrimSpace(r.FormValue("purchase_unit"))
	if purchaseUnit == "" {
		purchaseUnit = "each"
	}
	priceCents := int64(dollars * 100)
	user := middleware.UserFromCtx(r).Username

	// Transitional: keep the term-keyed manual_prices row for the fallback
	// pricing chain, and write the first-class per-store package.
	err = s.store.UpsertManualPrice(ctx, db.UpsertManualPriceParams{
		StoreID:        storeID,
		Region:         hh.ZIPCode,
		NormalizedTerm: item.NormalizedTerm,
		PriceCents:     priceCents,
		PackSize:       packSize,
		PurchaseUnit:   purchaseUnit,
		UpdatedBy:      user,
	})
	if err == nil {
		err = s.store.UpsertItemStorePackage(ctx, db.UpsertItemStorePackageParams{
			ItemID:           item.ID,
			StoreID:          storeID,
			PurchaseUnit:     purchaseUnit,
			AmountPerPackage: packSize,
			PriceCents:       priceCents,
			UpdatedBy:        user,
		})
	}
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Error saving price: %v", err))
	} else {
		s.setNotify(w, NotifySuccess, fmt.Sprintf("Price for %q saved.", item.Name))
	}
	http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
}

func (s *Server) handleAdminPriceDelete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if !s.owns(w, r, db.ResManualPrice, id) {
		return
	}
	_ = s.store.DeleteManualPrice(r.Context(), id)
	http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
}
