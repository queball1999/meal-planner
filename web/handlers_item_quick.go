package web

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"goeat/db"
	"goeat/middleware"
	"goeat/pricing"
)

// handleItemQuickAdd puts a catalog item straight onto the shopping list or
// into the pantry, without making the user retype what the catalog already
// knows.
//
// One handler for both destinations because they share everything except the
// final write: the same item lookup, the same quantity, the same price
// handling. Splitting them would mean maintaining that agreement twice.
//
// Price is optional. When it is left blank the existing pricing chain is not
// invoked here - a live lookup can take seconds and this is a button press -
// so the line goes on at whatever the catalog's own last known price was, or
// unpriced, and the normal re-price fills it in. Blocking a click on a network
// call would be the wrong trade.
func (s *Server) handleItemQuickAdd(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad item id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	item, err := s.store.GetItem(ctx, id)
	if err != nil || item == nil || item.HouseholdID != hh.ID {
		s.setNotify(w, NotifyDanger, "That item no longer exists.")
		http.Redirect(w, r, "/pantry/items", http.StatusSeeOther)
		return
	}

	dest := r.FormValue("destination")
	if dest != "list" && dest != "pantry" {
		s.setNotify(w, NotifyDanger, "Choose whether to add it to the list or the pantry.")
		http.Redirect(w, r, "/pantry/items", http.StatusSeeOther)
		return
	}

	qty := item.DefaultPurchaseQty
	if raw := strings.TrimSpace(r.FormValue("quantity")); raw != "" {
		if v, perr := strconv.ParseFloat(raw, 64); perr == nil && v > 0 {
			qty = v
		}
	}
	if qty <= 0 {
		qty = 1
	}

	var cents int64
	if raw := strings.TrimSpace(r.FormValue("price")); raw != "" {
		if v, perr := strconv.ParseFloat(raw, 64); perr == nil && v >= 0 {
			cents = int64(math.Round(v * 100))
		}
	}

	if dest == "pantry" {
		s.quickAddToPantry(w, r, hh, item, qty)
		return
	}
	s.quickAddToList(w, r, hh, item, qty, cents)
}

func (s *Server) quickAddToPantry(w http.ResponseWriter, r *http.Request, hh *db.Household, item *db.Item, qty float64) {
	ctx := r.Context()

	// CreatePantryItem's upsert adds to an existing quantity, which is what
	// "I bought more" means and exactly what this button is for.
	pi, err := s.store.CreatePantryItem(ctx, db.CreatePantryItemParams{
		HouseholdID:    hh.ID,
		Name:           item.Name,
		NormalizedTerm: item.NormalizedTerm,
		QuantityOnHand: qty,
		Unit:           item.StockUnit,
	})
	if err != nil {
		log.Printf("quick add pantry %d: %v", item.ID, err)
		s.setNotify(w, NotifyDanger, "Couldn't add that to the pantry.")
		http.Redirect(w, r, "/pantry/items", http.StatusSeeOther)
		return
	}
	if pi != nil && pi.ItemID == nil {
		_ = s.store.SetPantryItemItem(ctx, pi.ID, &item.ID)
	}

	s.setNotify(w, NotifySuccess, fmt.Sprintf("Added %s %s of %s to the pantry.", pricing.FormatQty(qty), item.StockUnit, item.Name))
	http.Redirect(w, r, r.FormValue("redirect_to"), http.StatusSeeOther)
}

func (s *Server) quickAddToList(w http.ResponseWriter, r *http.Request, hh *db.Household, item *db.Item, qty float64, cents int64) {
	ctx := r.Context()

	p, _ := s.store.GetLatestPlan(ctx, hh.ID)
	if p == nil {
		s.setNotify(w, NotifyWarning, "There's no shopping list yet - generate a plan first.")
		http.Redirect(w, r, r.FormValue("redirect_to"), http.StatusSeeOther)
		return
	}

	source, confidence := "manual", "manual"
	if cents == 0 {
		// Nothing typed: fall back to whatever price the app already knows for
		// this item, rather than putting a $0.00 line on the list.
		cents = s.lastKnownPriceCents(ctx, hh, item)
		if cents > 0 {
			source, confidence = "cache", "cached"
		} else {
			source, confidence = "estimate", "estimate"
		}
	}

	if _, err := s.store.CreateShoppingListItem(ctx, db.CreateShoppingListItemParams{
		PlanID:         p.ID,
		ItemID:         &item.ID,
		DisplayName:    item.Name,
		BuyQuantity:    qty,
		PackSize:       1,
		NeedQuantity:   qty,
		PurchaseUnit:   item.StockUnit,
		UnitPriceCents: cents,
		LineTotalCents: int64(math.Round(float64(cents) * qty)),
		PriceSource:    source,
		Confidence:     confidence,
	}); err != nil {
		log.Printf("quick add list %d: %v", item.ID, err)
		s.setNotify(w, NotifyDanger, "Couldn't add that to the shopping list.")
		http.Redirect(w, r, r.FormValue("redirect_to"), http.StatusSeeOther)
		return
	}

	msg := fmt.Sprintf("Added %s %s of %s to the shopping list.", pricing.FormatQty(qty), item.StockUnit, item.Name)
	if cents == 0 {
		msg += " No price found - set one from the list."
	}
	s.setNotify(w, NotifySuccess, msg)
	http.Redirect(w, r, r.FormValue("redirect_to"), http.StatusSeeOther)
}

// lastKnownPriceCents finds a price for an item without a live lookup: an
// operator-entered manual price first, then the most recent cached observation.
// Zero when the app has never seen a price for it.
func (s *Server) lastKnownPriceCents(ctx context.Context, hh *db.Household, item *db.Item) int64 {
	term := item.NormalizedTerm
	if term == "" {
		term = pricing.Normalize(item.Name)
	}
	stores, _ := s.store.ListStores(ctx, hh.ID)
	for _, st := range stores {
		if mp, _ := s.store.GetManualPrice(ctx, st.ID, hh.ZIPCode, term); mp != nil && mp.PriceCents > 0 {
			return mp.PriceCents
		}
	}
	for _, st := range stores {
		if pc, _ := s.store.GetPriceCache(ctx, st.ID, term); pc != nil && pc.PriceCents > 0 {
			return pc.PriceCents
		}
	}
	return 0
}
