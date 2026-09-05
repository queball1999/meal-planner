package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"goeat/db"
	"goeat/middleware"
)

type storesPageData struct {
	Stores []*db.GroceryStore
}

func (s *Server) handleStoresPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	stores, _ := s.store.ListStores(r.Context(), hh.ID)
	s.render(w, r, "stores", storesPageData{Stores: stores})
}

func (s *Server) handleStoreCreate(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	u := middleware.UserFromCtx(r)

	if err := r.ParseForm(); err != nil {
		s.setFlash(w, "Could not read form data.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.setFlash(w, "Store name is required.")
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
		s.setFlash(w, "Could not save store. Please try again.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	if u != nil {
		id := u.ID
		s.logEvent(r, &id, "create_store", "store", fmt.Sprintf("%d", store.ID), "")
	}
	s.setFlash(w, fmt.Sprintf("%q added.", store.Name))
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
		s.setFlash(w, "Invalid store ID.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	if err := s.store.DeleteStore(r.Context(), id); err != nil {
		s.setFlash(w, "Could not delete store.")
		http.Redirect(w, r, "/stores", http.StatusSeeOther)
		return
	}

	if u != nil {
		uid := u.ID
		s.logEvent(r, &uid, "delete_store", "store", fmt.Sprintf("%d", id), "")
	}
	s.setFlash(w, "Store removed.")
	http.Redirect(w, r, "/stores", http.StatusSeeOther)
}
