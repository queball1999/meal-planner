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
	"goeat/settings"
)

type scrapePageData struct {
	Stores []scrapeStoreRow
	HasLLM bool
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
	selectorsJSON := strings.TrimSpace(r.FormValue("selectors_json"))
	if selectorsJSON == "" {
		selectorsJSON = "{}"
	}

	// Reject unusable selectors at save time rather than silently returning no
	// prices later - a broken config is invisible until someone reads a plan.
	sels, perr := scrape.ParseSelectors(selectorsJSON)
	if perr != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Selectors are not valid JSON: %v", perr))
		http.Redirect(w, r, scrapeRedirect(r), http.StatusSeeOther)
		return
	}
	for _, spec := range []scrape.SelectorSpec{sels.Item, sels.Name, sels.Price, sels.PackSize} {
		if err := scrape.ValidateSelector(spec.CSS); err != nil {
			s.setNotify(w, NotifyDanger, fmt.Sprintf("Selector %q is not valid CSS: %v", spec.CSS, err))
			http.Redirect(w, r, scrapeRedirect(r), http.StatusSeeOther)
			return
		}
	}
	contextJSON, cerr := parseStoreContextForm(r)
	if cerr != nil {
		s.setNotify(w, NotifyDanger, cerr.Error())
		http.Redirect(w, r, scrapeRedirect(r), http.StatusSeeOther)
		return
	}

	if !strings.Contains(searchURL, "{term}") {
		s.setNotify(w, NotifyDanger, "Search URL must contain {term} - that is where the ingredient name is substituted.")
		http.Redirect(w, r, scrapeRedirect(r), http.StatusSeeOther)
		return
	}

	existing, _ := s.store.GetScrapeConfigByStore(ctx, storeID)
	if existing == nil {
		_, err = s.store.CreateScrapeConfig(ctx, db.CreateScrapeConfigParams{
			StoreID:           storeID,
			SearchURLTemplate: searchURL,
			SelectorsJSON:     selectorsJSON,
			Mode:              mode,
			ContextJSON:       contextJSON,
			AIAssisted:        aiAssisted,
		})
	} else {
		err = s.store.UpdateScrapeConfig(ctx, db.UpdateScrapeConfigParams{
			ID:                existing.ID,
			SearchURLTemplate: searchURL,
			SelectorsJSON:     selectorsJSON,
			Mode:              mode,
			ContextJSON:       contextJSON,
			AIAssisted:        aiAssisted,
			Status:            "active",
			LastTestedAt:      existing.LastTestedAt,
		})
	}
	if err != nil {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Error saving config: %v", err))
	} else {
		s.setNotify(w, NotifySuccess, "Scrape config saved.")
	}
	http.Redirect(w, r, scrapeRedirect(r), http.StatusSeeOther)
}

// parseStoreContextForm builds the store context from the three friendly
// fields the config form shows (cookies as NAME=value lines, one pre-warm URL
// per line, a selector to wait for) rather than asking an operator to hand-
// write JSON. It returns the JSON to store, or the message to show them.
func parseStoreContextForm(r *http.Request) (string, error) {
	sc := scrape.StoreContext{
		WaitFor: strings.TrimSpace(r.FormValue("context_wait_for")),
	}

	for _, line := range strings.Split(r.FormValue("context_cookies"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return "", fmt.Errorf("store cookie %q must be written as NAME=value", line)
		}
		if sc.Cookies == nil {
			sc.Cookies = map[string]string{}
		}
		// A cookie copied out of devtools often arrives with a trailing ";".
		sc.Cookies[name] = strings.TrimSuffix(strings.TrimSpace(value), ";")
	}

	for _, line := range strings.Split(r.FormValue("context_prewarm"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			return "", fmt.Errorf("pre-warm URL %q must start with http:// or https://", line)
		}
		sc.Prewarm = append(sc.Prewarm, line)
	}

	if err := scrape.ValidateSelector(sc.WaitFor); err != nil {
		return "", fmt.Errorf("wait-for selector %q is not valid CSS: %v", sc.WaitFor, err)
	}
	if sc.Empty() {
		return "{}", nil
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// scrapeRedirect picks where a config save returns to - the per-store gear
// modal on /stores posts to the same endpoint as the full config page.
func scrapeRedirect(r *http.Request) string {
	if r.FormValue("redirect_to") == "/stores" {
		return "/stores"
	}
	return "/admin/scrape"
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

	term := strings.TrimSpace(r.FormValue("term"))
	if term == "" {
		term = "eggs"
	}
	testURL := strings.ReplaceAll(cfg.SearchURLTemplate, "{term}", url.QueryEscape(term))
	fetchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	result, fetchErr := s.fetchForScrape(fetchCtx, testURL)
	if fetchErr == nil && result.Challenge != "" {
		fetchErr = fmt.Errorf("store blocked the fetch: %s", result.Challenge)
	}

	status := "degraded"
	var products []scrape.ExtractedProduct
	if fetchErr == nil {
		sels, _ := scrape.ParseSelectors(cfg.SelectorsJSON)
		products = scrape.Extract(result.HTML, sels, 3)
		// Auto + AI configs may have no selectors at all: the model reads the
		// page. Prove that path works during the test too.
		if len(products) == 0 && cfg.Mode == "auto_ai" && s.gen != nil {
			products, _ = scrape.ExtractWithAI(fetchCtx, s.gen, result.HTML, term)
		}
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
		"error": func() string {
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

	// Long enough for both headless browsers to have a turn: each render has
	// its own 45s budget and the chain may use two of them.
	ctx, cancel := context.WithTimeout(r.Context(), renderChainTimeout)
	defer cancel()

	// The same path the scraper itself uses, so what the operator previews is
	// what a lookup would see - including the escalation to FlareSolverr or
	// Browserless. The old code fetched directly and only knew about the
	// legacy FLARESOLVERR_URL, so a blocked store previewed as its bot wall.
	res, err := s.fetchForScrape(ctx, rawURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("fetch error: %v", err), http.StatusBadGateway)
		return
	}

	stripped := scrape.StripScripts(res.HTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Tell the preview UI how the page was obtained, and whether it is still a
	// wall, so it can say so instead of rendering the challenge as if it were
	// the store.
	if res.Backend != "" {
		w.Header().Set("X-Scrape-Backend", res.Backend)
	}
	if res.Challenge != "" {
		w.Header().Set("X-Scrape-Challenge", res.Challenge)
	}
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

	ctx, cancel := context.WithTimeout(r.Context(), renderChainTimeout)
	defer cancel()

	// Same fetch path as the preview, or a selector picked off a rendered
	// page would be verified against the un-rendered one.
	res, err := s.fetchForScrape(ctx, rawURL)
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

// handleScrapeAutodetect fetches a page and proposes a scrape config: first
// structured data and built-in markup heuristics, then - when the caller asked
// for Auto + AI - the LLM detector, whose selectors are verified against the
// live page before they are offered (§6.7).
func (s *Server) handleScrapeAutodetect(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}

	// The page fetch alone can use two renderers; the AI pass then needs its
	// own time on top.
	ctx, cancel := context.WithTimeout(r.Context(), renderChainTimeout+90*time.Second)
	defer cancel()

	res, err := s.fetchForScrape(ctx, rawURL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"confidence": "none",
			"notes":      fmt.Sprintf("Could not fetch the page: %v", err),
		})
		return
	}
	if res.Challenge != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"confidence": "none",
			"notes":      "The store blocked the fetch: " + res.Challenge + ". " + s.blockedAdvice(ctx),
		})
		return
	}

	wantAI := r.FormValue("ai_assisted") == "1"
	var proposal *scrape.Proposal
	if wantAI && s.gen != nil {
		proposal, err = scrape.ProposeWithAI(ctx, s.gen, res.HTML)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"confidence": "none",
				"notes":      fmt.Sprintf("AI detection failed: %v", err),
			})
			return
		}
	} else {
		proposal = scrape.Detect(res.HTML)
		if wantAI && s.gen == nil {
			proposal.Notes += " (No LLM is configured, so AI detection was skipped.)"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":           proposal.Mode,
		"confidence":     proposal.Confidence,
		"notes":          proposal.Notes,
		"attempts":       proposal.Attempts,
		"selectors_json": proposal.SelectorsJSON(),
		"products":       proposal.Products,
	})
}

// handleScrapeAIExtract reads prices off a page with the LLM directly, without
// selectors. Exposed as the "Ask AI to read this page" button for stores whose
// markup is too unstable to pin down.
func (s *Server) handleScrapeAIExtract(w http.ResponseWriter, r *http.Request) {
	if s.gen == nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": "No LLM is configured."})
		return
	}
	rawURL := strings.TrimSpace(r.FormValue("url"))
	term := strings.TrimSpace(r.FormValue("term"))
	if rawURL == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	if term == "" {
		term = "eggs"
	}

	ctx, cancel := context.WithTimeout(r.Context(), renderChainTimeout+90*time.Second)
	defer cancel()

	res, err := s.fetchForScrape(ctx, rawURL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("fetch failed: %v", err)})
		return
	}
	if res.Challenge != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"error": "The store blocked the fetch: " + res.Challenge + ". " + s.blockedAdvice(ctx),
		})
		return
	}

	products, err := scrape.ExtractWithAI(ctx, s.gen, res.HTML, term)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": products, "term": term})
}

// blockedAdvice is the next step to suggest when a store answers with a bot
// wall. Telling an operator who already runs both headless browsers to "set
// one up" is worse than saying nothing - past that point the honest answer is
// that this particular store is not scrapeable from here.
func (s *Server) blockedAdvice(ctx context.Context) string {
	rc := s.renderConfig(ctx)
	if !rc.Enabled() {
		return "Configure a headless browser under Settings \u2192 Scraping and try again."
	}
	return "Both " + rc.Label() + " were tried and both were blocked. Enterprise anti-bot " +
		"services (Imperva, PerimeterX, DataDome) fingerprint datacentre IPs and headless " +
		"Chrome, so this store usually needs a residential-proxy unblocker - or price it " +
		"with Manual prices, or let the AI estimate it."
}

// renderChainTimeout budgets a direct fetch plus a turn at each configured
// headless browser (scrape.renderTimeout is 45s per service).
const renderChainTimeout = 110 * time.Second

// renderConfig resolves the headless-browser settings for this request.
func (s *Server) renderConfig(ctx context.Context) scrape.RenderConfig {
	return settings.LiveRenderConfig(ctx, s.store, s.cfg)
}

// fetchForScrape fetches a page the way the scraper does: browser headers, a
// cookie jar, and an escalation to the configured headless browser when the
// site answers with a challenge or a JavaScript shell.
func (s *Server) fetchForScrape(ctx context.Context, rawURL string) (*scrape.SmartResult, error) {
	return scrape.FetchSmart(ctx, rawURL, s.renderConfig(ctx))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
