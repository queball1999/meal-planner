package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"goeat/db"
	"goeat/middleware"
)

type storesPageData struct {
	Stores      []storeRow
	Catalog     []catalogEntry
	HasLLM      bool
	KrogerReady bool
	State       string // two-letter state from the household ZIP; "" = unknown
	AwayCount   int    // catalog chains that do not operate in State
}

// storeRow pairs a household store with its scrape config (nil when the store
// has never been configured) so the gear modal can be pre-filled.
type storeRow struct {
	Store  *db.GroceryStore
	Config *db.ScrapeConfig
	Known  *KnownStore // nil for hand-typed stores not in the catalog
}

// catalogEntry is one chip in the "where do you shop" picker - the same list
// used by the setup wizard, plus whether the household already has it and
// whether the chain reaches the household's state.
type catalogEntry struct {
	KnownStore
	Selected  bool
	Available bool
}

func (s *Server) handleStoresPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	stores, _ := s.store.ListStores(ctx, hh.ID)

	rows := make([]storeRow, 0, len(stores))
	have := make(map[string]bool, len(stores))
	for _, gs := range stores {
		have[gs.Name] = true
		cfg, _ := s.store.GetScrapeConfigByStore(ctx, gs.ID)
		rows = append(rows, storeRow{Store: gs, Config: cfg, Known: KnownStoreByName(gs.Name)})
	}

	// Chains only operate in some states, so a Phoenix household should not be
	// offered Publix. Stores already added stay visible whatever the ZIP says -
	// the household knows where it shops better than this table does.
	state := StateForZIP(hh.ZIPCode)
	catalog := make([]catalogEntry, 0, len(KnownStores))
	away := 0
	for _, ks := range KnownStores {
		ok := ks.AvailableIn(state) || have[ks.Name]
		if !ok {
			away++
		}
		catalog = append(catalog, catalogEntry{
			KnownStore: ks,
			Selected:   have[ks.Name],
			Available:  ok,
		})
	}

	s.render(w, r, "stores", storesPageData{
		Stores:      rows,
		Catalog:     catalog,
		HasLLM:      s.gen != nil,
		KrogerReady: s.cfg.KrogerClientID != "",
		State:       state,
		AwayCount:   away,
	})
}

// handleStoreSelect syncs the household's stores against the catalog picker:
// checked catalog stores are created if missing, unchecked ones are removed.
// Stores that are not in the catalog (hand-typed) are never touched.
func (s *Server) handleStoreSelect(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	u := middleware.UserFromCtx(r)
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Could not read form data.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	wanted := make(map[string]bool)
	for _, name := range r.Form["stores"] {
		if KnownStoreByName(name) != nil {
			wanted[name] = true
		}
	}

	existing, _ := s.store.ListStores(ctx, hh.ID)
	have := make(map[string]bool, len(existing))
	var added, removed int

	for _, gs := range existing {
		have[gs.Name] = true
		if KnownStoreByName(gs.Name) == nil || wanted[gs.Name] {
			continue // custom store, or still selected
		}
		if err := s.store.DeleteStore(ctx, gs.ID); err == nil {
			removed++
			if u != nil {
				uid := u.ID
				s.logEvent(r, &uid, "delete_store", "store", fmt.Sprintf("%d", gs.ID), gs.Name)
			}
		}
	}

	for _, ks := range KnownStores {
		if !wanted[ks.Name] || have[ks.Name] {
			continue
		}
		store, err := s.store.CreateStore(ctx, db.UpsertStoreParams{
			HouseholdID: hh.ID,
			Name:        ks.Name,
			Kind:        ks.Kind,
		})
		if err != nil {
			continue
		}
		added++
		s.ensureScrapeConfig(ctx, store.ID, KnownStoreByName(ks.Name))
		if u != nil {
			uid := u.ID
			s.logEvent(r, &uid, "create_store", "store", fmt.Sprintf("%d", store.ID), ks.Name)
		}
	}

	switch {
	case added > 0 && removed > 0:
		s.setNotify(w, NotifySuccess, fmt.Sprintf("%d store(s) added, %d removed.", added, removed))
	case added > 0:
		s.setNotify(w, NotifySuccess, fmt.Sprintf("%d store(s) added.", added))
	case removed > 0:
		s.setNotify(w, NotifySuccess, fmt.Sprintf("%d store(s) removed.", removed))
	default:
		s.setNotify(w, NotifyInfo, "No changes to your stores.")
	}
	http.Redirect(w, r, "/stores", http.StatusSeeOther)
}

func (s *Server) handleStoreCreate(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	u := middleware.UserFromCtx(r)

	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Could not read form data.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.setNotify(w, NotifyDanger, "Store name is required.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	kind := r.FormValue("kind")
	if kind == "" {
		kind = "grocery"
	}
	store, err := s.store.CreateStore(r.Context(), db.UpsertStoreParams{
		HouseholdID: hh.ID,
		Name:        name,
		Kind:        kind,
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, "Could not save store. Please try again.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	s.ensureScrapeConfig(r.Context(), store.ID, KnownStoreByName(store.Name))
	if u != nil {
		id := u.ID
		s.logEvent(r, &id, "create_store", "store", fmt.Sprintf("%d", store.ID), "")
	}
	s.setNotify(w, NotifySuccess, fmt.Sprintf("%q added.", store.Name))
	http.Redirect(w, r, "/stores", http.StatusSeeOther)
}

func (s *Server) handleStoreDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	u := middleware.UserFromCtx(r)

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		s.setNotify(w, NotifyDanger, "Invalid store ID.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	if err := s.store.DeleteStore(r.Context(), id); err != nil {
		s.setNotify(w, NotifyDanger, "Could not delete store.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	if u != nil {
		uid := u.ID
		s.logEvent(r, &uid, "delete_store", "store", fmt.Sprintf("%d", id), "")
	}
	s.setNotify(w, NotifySuccess, "Store removed.")
	http.Redirect(w, r, "/stores", http.StatusSeeOther)
}

// ensureScrapeConfig gives a newly added catalog store a starting scrape
// config: the chain's real search URL, and the most capable mode this
// deployment supports. Without it every new store lands in Settings with an
// empty URL box the operator has to go hunt for. Never overwrites an existing
// config.
func (s *Server) ensureScrapeConfig(ctx context.Context, storeID int64, ks *KnownStore) {
	if ks == nil || ks.SearchURL == "" {
		return
	}
	if existing, _ := s.store.GetScrapeConfigByStore(ctx, storeID); existing != nil {
		return
	}
	mode := "auto"
	if s.gen != nil {
		mode = "auto_ai" // the model can read the page when selectors fail
	}
	if _, err := s.store.CreateScrapeConfig(ctx, db.CreateScrapeConfigParams{
		StoreID:           storeID,
		SearchURLTemplate: ks.SearchURL,
		SelectorsJSON:     "{}",
		Mode:              mode,
		AIAssisted:        s.gen != nil,
	}); err != nil {
		log.Printf("stores: prefill scrape config store=%d: %v", storeID, err)
	}
}

// handleZIPState answers the setup wizard's "which stores exist near this ZIP"
// question. Returns {"state":"AZ"} - an empty state means "could not place it",
// and the client then shows the whole catalog.
func (s *Server) handleZIPState(w http.ResponseWriter, r *http.Request) {
	state := StateForZIP(r.URL.Query().Get("zip"))
	unavailable := []string{}
	for _, ks := range KnownStores {
		if !ks.AvailableIn(state) {
			unavailable = append(unavailable, ks.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":       state,
		"unavailable": unavailable,
	})
}
