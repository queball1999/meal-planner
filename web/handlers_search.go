package web

import (
	"net/http"
	"strings"

	"goeat/middleware"
	"goeat/search"
)

type searchPageData struct {
	Q       string
	Recipes []search.Result
	Pantry  []search.Result
	Meals   []search.Result
	Empty   bool
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	opts := search.Options{
		Partial: true,
		Fuzzy:   r.URL.Query().Get("fuzzy") == "1",
	}

	results, isExact, err := search.Search(r.Context(), s.store, hh.ID, q, opts)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Search error: "+err.Error())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if isExact && len(results) == 1 {
		http.Redirect(w, r, results[0].URL, http.StatusSeeOther)
		return
	}

	pd := searchPageData{Q: q, Empty: len(results) == 0}
	for _, r := range results {
		switch r.Kind {
		case "recipe":
			pd.Recipes = append(pd.Recipes, r)
		case "pantry":
			pd.Pantry = append(pd.Pantry, r)
		case "meal":
			pd.Meals = append(pd.Meals, r)
		}
	}
	s.render(w, r, "search_results", pd)
}
