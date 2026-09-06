package web

import (
	"net/http"
	"strings"

	"goeat/barcode"
	"goeat/db"
	"goeat/middleware"
	"goeat/pricing"
)

type scanPageData struct {
	Code string // non-empty = unknown barcode, offer to add to pantry
}

// handleScanPage renders the camera-scan UI (linked from pantry).
func (s *Server) handleScanPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "scan", scanPageData{})
}

// handleScanCode resolves a scanned barcode and redirects to its destination.
func (s *Server) handleScanCode(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	code := strings.TrimSpace(r.PathValue("code"))
	if code == "" {
		http.Redirect(w, r, "/scan", http.StatusSeeOther)
		return
	}

	match, err := barcode.Lookup(r.Context(), s.store, hh.ID, code)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Barcode lookup error: "+err.Error())
		http.Redirect(w, r, "/pantry", http.StatusSeeOther)
		return
	}
	if match != nil {
		http.Redirect(w, r, match.URL, http.StatusSeeOther)
		return
	}

	// Unknown barcode - render page offering to add to pantry.
	s.render(w, r, "scan", scanPageData{Code: code})
}

// handlePantryScan handles POST /pantry/scan: adds or increments a pantry item by barcode.
func (s *Server) handlePantryScan(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	code := strings.TrimSpace(r.FormValue("barcode"))
	if code == "" {
		http.Redirect(w, r, "/pantry", http.StatusSeeOther)
		return
	}

	ctx := r.Context()

	existing, _ := s.store.GetPantryItemByBarcode(ctx, hh.ID, code)
	if existing != nil {
		_ = s.store.IncrementPantryItem(ctx, existing.ID, 1)
		s.setNotify(w, NotifySuccess, "Added 1 "+existing.Name+" to pantry.")
	} else {
		normalized := pricing.Normalize(code)
		_, _ = s.store.CreatePantryItem(ctx, db.CreatePantryItemParams{
			HouseholdID:    hh.ID,
			Name:           code,
			NormalizedTerm: normalized,
			QuantityOnHand: 1,
			Unit:           "each",
			Barcode:        code,
		})
		s.setNotify(w, NotifySuccess, "Scanned "+code+" added - edit the name in the pantry.")
	}
	http.Redirect(w, r, "/pantry", http.StatusSeeOther)
}
