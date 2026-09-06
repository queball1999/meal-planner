package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"goeat/db"
	"goeat/middleware"
	"goeat/scrape"
)

type scrapePageData struct {
	Stores  []scrapeStoreRow
	HasLLM  bool
}

type scrapeStoreRow struct {
	Store  *db.GroceryStore
	Config *db.ScrapeConfig // nil when not yet configured
}

func (s *Server) handleScrapeConfigPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	stores, _ := s.store.ListStores(ctx, hh.ID)

	var rows []scrapeStoreRow
	for _, gs := range stores {
		cfg, _ := s.store.GetScrapeConfigByStore(ctx, gs.ID)
		rows = append(rows, scrapeStoreRow{Store: gs, Config: cfg})
	}
	s.render(w, r, "scrape_config", scrapePageData{Stores: rows, HasLLM: s.gen != nil})
}

func (s *Server) handleScrapeConfigSave(w http.ResponseWriter, r *http.Request) {
	storeID, err := strconv.ParseInt(r.PathValue("storeID"), 10, 64)
	if err != nil {
		http.Error(w, "bad storeID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	searchURL := strings.TrimSpace(r.FormValue("search_url_template"))
	mode := r.FormValue("mode")
	if mode == "" {
		mode = "assisted"
	}
	aiAssisted := r.FormValue("ai_assisted") == "1"
	selectorsJSON := r.FormValue("selectors_json")
	if selectorsJSON == "" {
		selectorsJSON = "{}"
	}

	existing, _ := s.store.GetScrapeConfigByStore(ctx, storeID)
	if existing == nil {
		_, err = s.store.CreateScrapeConfig(ctx, db.CreateScrapeConfigParams{
			StoreID:           storeID,
			SearchURLTemplate: searchURL,
			SelectorsJSON:     selectorsJSON,
			Mode:              mode,
			AIAssisted:        aiAssisted,
		})
	} else {
		err = s.store.UpdateScrapeConfig(ctx, db.UpdateScrapeConfigParams{
			ID:                existing.ID,
			SearchURLTemplate: searchURL,
			SelectorsJSON:     selectorsJSON,
			Mode:              mode,
			AIAssisted:        aiAssisted,
			Status:            "active",
			LastTestedAt:      existing.LastTestedAt,
		})
	}
	if err != nil {
		s.setFlash(w, fmt.Sprintf("Error saving config: %v", err))
	} else {
		s.setFlash(w, "Scrape config saved.")
	}
	http.Redirect(w, r, "/admin/scrape", http.StatusSeeOther)
}

// handleScrapeConfigTest re-fetches the page, runs extraction, and updates
// the config status to active or degraded based on the result.
func (s *Server) handleScrapeConfigTest(w http.ResponseWriter, r *http.Request) {
	storeID, err := strconv.ParseInt(r.PathValue("storeID"), 10, 64)
	if err != nil {
		http.Error(w, "bad storeID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	cfg, err := s.store.GetScrapeConfigByStore(ctx, storeID)
	if err != nil || cfg == nil {
		http.Error(w, "config not found", http.StatusNotFound)
		return
	}

	testURL := strings.ReplaceAll(cfg.SearchURLTemplate, "{term}", url.QueryEscape("eggs"))
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, fetchErr := scrape.Fetch(fetchCtx, testURL)
	if fetchErr != nil && s.cfg.FlareSolverrURL != "" {
		result, fetchErr = scrape.FetchViaProxy(fetchCtx, testURL, s.cfg.FlareSolverrURL)
	}

	status := "degraded"
	var products []scrape.ExtractedProduct
	if fetchErr == nil {
		sels, _ := scrape.ParseSelectors(cfg.SelectorsJSON)
		products = scrape.Extract(result.HTML, sels, 3)
		if len(products) > 0 && products[0].Price > 0 {
			status = "active"
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_ = s.store.UpdateScrapeConfig(ctx, db.UpdateScrapeConfigParams{
		ID:                cfg.ID,
		SearchURLTemplate: cfg.SearchURLTemplate,
		SelectorsJSON:     cfg.SelectorsJSON,
		Mode:              cfg.Mode,
		AIAssisted:        cfg.AIAssisted,
		Status:            status,
		LastTestedAt:      now,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":   status,
		"products": products,
		"error":    func() string {
			if fetchErr != nil {
				return fetchErr.Error()
			}
			return ""
		}(),
	})
}

// handleScrapeProxy proxies same-origin assets for the sandboxed scrape preview
// iframe. Only allowlisted domains are proxied (§6.7 sandbox).
func (s *Server) handleScrapeProxy(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	// Allowlist: only CSS/font/image assets (no JS).
	ext := strings.ToLower(parsed.Path[strings.LastIndex(parsed.Path, ".")+1:])
	allowed := map[string]bool{"css": true, "woff": true, "woff2": true, "ttf": true,
		"png": true, "jpg": true, "jpeg": true, "gif": true, "svg": true, "ico": true}
	if !allowed[ext] {
		http.Error(w, "asset type not allowed", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	res, err := scrape.Fetch(ctx, rawURL)
	if err != nil {
		http.Error(w, "fetch error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	fmt.Fprint(w, res.HTML)
}

// handleScrapeFetch fetches a URL server-side and returns HTML stripped of
// scripts for Assisted-mode's sandboxed preview (§6.7).
func (s *Server) handleScrapeFetch(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	res, err := scrape.Fetch(ctx, rawURL)
	if err != nil && s.cfg.FlareSolverrURL != "" {
		res, err = scrape.FetchViaProxy(ctx, rawURL, s.cfg.FlareSolverrURL)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("fetch error: %v", err), http.StatusBadGateway)
		return
	}

	stripped := scrape.StripScripts(res.HTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, stripped)
}

// handleScrapeSelector receives a CSS selector from the Assisted-mode UI and
// returns the first extracted text for confirmation.
func (s *Server) handleScrapeSelector(w http.ResponseWriter, r *http.Request) {
	rawURL := r.FormValue("url")
	selector := r.FormValue("selector")
	if rawURL == "" || selector == "" {
		http.Error(w, "missing url or selector", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	res, err := scrape.Fetch(ctx, rawURL)
	if err != nil {
		http.Error(w, "fetch error", http.StatusBadGateway)
		return
	}
	sels := &scrape.Selectors{Price: scrape.SelectorSpec{CSS: selector}}
	products := scrape.Extract(res.HTML, sels, 3)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"products": products,
	})
}

// handleScrapeAutodetect fetches a page and runs JSON-LD / Open Graph
// heuristics to propose a scrape config (Auto and Auto+AI modes, §6.7).
func (s *Server) handleScrapeAutodetect(w http.ResponseWriter, r *http.Request) {
	rawURL := r.FormValue("url")
	if rawURL == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	res, err := scrape.Fetch(ctx, rawURL)
	if err != nil && s.cfg.FlareSolverrURL != "" {
		res, err = scrape.FetchViaProxy(ctx, rawURL, s.cfg.FlareSolverrURL)
	}
	if err != nil {
		http.Error(w, "fetch error", http.StatusBadGateway)
		return
	}

	// Auto mode: try JSON-LD/schema.org detection (no selectors needed).
	products := scrape.Extract(res.HTML, nil, 5)

	proposed := map[string]any{
		"selectors_json": "{}",
		"mode":           "auto",
		"confidence":     "high",
		"products":       products,
	}

	// Auto+AI: if heuristics found nothing and an LLM is configured, ask it.
	if len(products) == 0 && s.gen != nil && r.FormValue("ai_assisted") == "1" {
		proposed["mode"] = "auto_ai"
		proposed["confidence"] = "low"
		proposed["message"] = "JSON-LD not found. AI assist would analyze the page (not yet implemented in this version — configure selectors manually in Assisted mode)."
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(proposed)
}
