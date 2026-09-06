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

type adminPricesPageData struct {
	Stores []storeWithPrices
}

type manualPriceRow struct {
	*db.ManualPrice
	PriceLabel string
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

	var rows []storeWithPrices
	for _, gs := range stores {
		rawPrices, _ := s.store.ListManualPrices(ctx, gs.ID)
		var priceRows []manualPriceRow
		for _, p := range rawPrices {
			priceRows = append(priceRows, manualPriceRow{
				ManualPrice: p,
				PriceLabel:  fmt.Sprintf("$%.2f", float64(p.PriceCents)/100),
			})
		}
		priceRows, page := paginateNamed(r, priceRows, fmt.Sprintf("page_%d", gs.ID))
		rows = append(rows, storeWithPrices{Store: gs, Prices: priceRows, Page: page})
	}
	s.render(w, r, "admin_prices", adminPricesPageData{Stores: rows})
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

	rawName := strings.TrimSpace(r.FormValue("name"))
	if rawName == "" {
		s.setNotify(w, NotifyDanger, "Ingredient name is required.")
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

	packSizeStr := strings.TrimSpace(r.FormValue("pack_size"))
	packSize, _ := strconv.ParseFloat(packSizeStr, 64)
	if packSize <= 0 {
		packSize = 1
	}

	normalized := pricing.Normalize(rawName)
	err = s.store.UpsertManualPrice(r.Context(), db.UpsertManualPriceParams{
		StoreID:        storeID,
		Region:         hh.ZIPCode,
		NormalizedTerm: normalized,
		PriceCents:     int64(dollars * 100),
		PackSize:       packSize,
		PurchaseUnit:   strings.TrimSpace(r.FormValue("purchase_unit")),
		UpdatedBy:      middleware.UserFromCtx(r).Username,
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Error saving price: %v", err))
	} else {
		s.setNotify(w, NotifySuccess, fmt.Sprintf("Price for %q saved.", rawName))
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
	_ = s.store.DeleteManualPrice(r.Context(), id)
	http.Redirect(w, r, "/admin/prices", http.StatusSeeOther)
}
