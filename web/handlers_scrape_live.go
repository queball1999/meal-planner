package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"

	"goeat/db"
	"goeat/scrape"
	"goeat/scrape/live"
)

// This file is the hybrid-CAPTCHA-solve web layer: when the automated
// FlareSolverr/Browserless chain still comes back bot-walled (see
// scrape.IsBotWall, pricing.ScraperProvider.Lookup), the Scrape Config page
// offers an admin two ways to unblock a store themselves - a live CDP
// session (scrape/live) they watch and click through, or pasting cookies
// they solved elsewhere. Either way the result is a scrape_clearances row
// ScraperProvider.Lookup tries ahead of the automated chain from then on.

// clearanceTTL is how long a solved clearance is trusted before the
// automated chain is asked to prove it can get in on its own again. Most
// anti-bot cookies (Cloudflare's cf_clearance included) are valid for
// hours, not minutes; 2h is a conservative middle ground that avoids both
// re-solving too often and trusting a cookie long after the site has
// rotated it.
const clearanceTTL = 2 * time.Hour

// defaultLiveTerm is the placeholder ingredient used to build a search URL
// for a live session, matching handleScrapeConfigTest's own default - any
// real search page works for solving a site-wide challenge, and one is
// needed since ScrapeConfig only stores a {term} template.
const defaultLiveTerm = "eggs"

// liveSessionEntry pairs a running CDP session with the store it's solving
// for, so Finish knows where to persist the resulting clearance.
type liveSessionEntry struct {
	session *live.Session
	storeID int64
}

// liveSessionRegistry is the in-memory map of in-flight live-solve sessions,
// keyed by a random id handed to the admin's browser - mirrors
// plan/pricing_cancel.go's package-level registry pattern. Sessions are
// self-cleaning (live.Session's own idle timer), so a leaked map entry from
// a browser that never called finish/cancel just accumulates until the
// process restarts; not worth a sweep goroutine for what is, in practice, a
// handful of admins solving the occasional CAPTCHA.
type liveSessionRegistry struct {
	mu sync.Mutex
	m  map[string]*liveSessionEntry
}

var liveSessions = &liveSessionRegistry{m: make(map[string]*liveSessionEntry)}

func (r *liveSessionRegistry) put(id string, e *liveSessionEntry) {
	r.mu.Lock()
	r.m[id] = e
	r.mu.Unlock()
}

func (r *liveSessionRegistry) get(id string) (*liveSessionEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[id]
	return e, ok
}

func (r *liveSessionRegistry) delete(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

// handleScrapeLiveStart opens a live CDP session against storeID's search
// page and returns a session id the admin's browser uses for the ws/finish/
// cancel calls below.
func (s *Server) handleScrapeLiveStart(w http.ResponseWriter, r *http.Request) {
	storeID, err := strconv.ParseInt(r.PathValue("storeID"), 10, 64)
	if err != nil {
		http.Error(w, "bad storeID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	cfg, err := s.store.GetScrapeConfigByStore(ctx, storeID)
	if err != nil || cfg == nil || cfg.SearchURLTemplate == "" {
		writeLiveJSONError(w, http.StatusNotFound, "no scrape config for this store")
		return
	}

	var browserless scrape.Renderer
	for _, rr := range s.renderConfig(ctx).Renderers() {
		if rr.Backend == scrape.RendererBrowserless {
			browserless = rr
			break
		}
	}
	if !browserless.Enabled() {
		writeLiveJSONError(w, http.StatusBadRequest,
			"no Browserless renderer configured - paste cookies instead, or set one up under Settings → Scraping")
		return
	}

	targetURL := strings.ReplaceAll(cfg.SearchURLTemplate, "{term}", url.QueryEscape(defaultLiveTerm))
	sess, err := live.StartSession(ctx, browserless, targetURL)
	if err != nil {
		writeLiveJSONError(w, http.StatusBadGateway, fmt.Sprintf("could not start a live session: %v", err))
		return
	}

	id := uuid.NewString()
	liveSessions.put(id, &liveSessionEntry{session: sess, storeID: storeID})

	writeLiveJSON(w, map[string]any{"session_id": id})
}

// liveWSOutMessage is one frame relayed to the admin's browser.
type liveWSOutMessage struct {
	Type   string `json:"type"` // "frame"
	Data   string `json:"data,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// liveWSInMessage is one input event from the admin's browser.
type liveWSInMessage struct {
	Type string  `json:"type"` // "click" | "mousemove" | "keydown"
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	Key  string  `json:"key,omitempty"`
	Text string  `json:"text,omitempty"`
}

const liveInputTimeout = 5 * time.Second

// handleScrapeLiveWS is the admin browser's own websocket: relays screencast
// frames out, forwards clicks/keys back into the CDP session. A separate
// connection from the one scrape/live holds to Browserless - this package
// never talks CDP directly.
func (s *Server) handleScrapeLiveWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	entry, ok := liveSessions.get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	websocket.Handler(func(ws *websocket.Conn) {
		relayLiveSession(ws, entry)
	}).ServeHTTP(w, r)
}

// relayLiveSession runs for the lifetime of one admin websocket connection:
// a frame-relay goroutine alongside the calling goroutine's input-read loop,
// both exiting together once either side disconnects.
func relayLiveSession(ws *websocket.Conn, entry *liveSessionEntry) {
	done := make(chan struct{})
	var closeOnce sync.Once
	stop := func() { closeOnce.Do(func() { close(done) }) }
	defer stop()

	go func() {
		for {
			select {
			case frame, ok := <-entry.session.Frames:
				if !ok {
					stop()
					return
				}
				out := liveWSOutMessage{Type: "frame", Data: frame.JPEGBase64, Width: frame.Width, Height: frame.Height}
				if err := websocket.JSON.Send(ws, out); err != nil {
					stop()
					return
				}
			case <-entry.session.Done():
				stop()
				return
			case <-done:
				return
			}
		}
	}()

	for {
		var in liveWSInMessage
		if err := websocket.JSON.Receive(ws, &in); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), liveInputTimeout)
		switch in.Type {
		case "click":
			_ = entry.session.Click(ctx, in.X, in.Y)
		case "mousemove":
			_ = entry.session.MouseMove(ctx, in.X, in.Y)
		case "keydown":
			_ = entry.session.KeyEvent(ctx, in.Key, in.Text)
		}
		cancel()

		select {
		case <-done:
			return
		default:
		}
	}
}

// handleScrapeLiveFinish is called once the admin believes they've solved
// the challenge: harvests the session's cookies, persists them as this
// store's clearance, clears its blocked state, and tears the session down.
func (s *Server) handleScrapeLiveFinish(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	entry, ok := liveSessions.get(sessionID)
	if !ok {
		writeLiveJSONError(w, http.StatusNotFound, "session not found or already finished")
		return
	}

	ctx := r.Context()
	clearance, err := entry.session.Finish(ctx)
	if err != nil {
		// Not necessarily fatal - most likely the challenge isn't solved yet
		// (no cookies to hand over). Leave the session open so the admin can
		// keep trying and hit "I solved it" again.
		writeLiveJSONError(w, http.StatusConflict, err.Error())
		return
	}

	if err := s.persistClearance(ctx, entry.storeID, clearance); err != nil {
		writeLiveJSONError(w, http.StatusInternalServerError, fmt.Sprintf("solved, but could not save it: %v", err))
		return
	}

	liveSessions.delete(sessionID)
	entry.session.Close()

	writeLiveJSON(w, map[string]any{"ok": true})
}

// handleScrapeLiveCancel abandons a live session without saving anything.
// Idempotent - a session already finished or timed out is simply not found.
func (s *Server) handleScrapeLiveCancel(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if entry, ok := liveSessions.get(sessionID); ok {
		liveSessions.delete(sessionID)
		entry.session.Close()
	}
	writeLiveJSON(w, map[string]any{"ok": true})
}

// handleScrapeClearanceManual is the fallback path when Browserless isn't
// configured (or the live view didn't work): an admin solves the challenge
// in their own browser, then pastes the resulting cookies here.
func (s *Server) handleScrapeClearanceManual(w http.ResponseWriter, r *http.Request) {
	storeID, err := strconv.ParseInt(r.PathValue("storeID"), 10, 64)
	if err != nil {
		http.Error(w, "bad storeID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	cfg, err := s.store.GetScrapeConfigByStore(ctx, storeID)
	if err != nil || cfg == nil {
		writeLiveJSONError(w, http.StatusNotFound, "no scrape config for this store")
		return
	}

	raw := strings.TrimSpace(r.FormValue("cookies"))
	if raw == "" {
		writeLiveJSONError(w, http.StatusBadRequest, "paste the cookies from your browser's dev tools first")
		return
	}
	userAgent := strings.TrimSpace(r.FormValue("user_agent"))

	domain := scrape.CookieDomain(strings.ReplaceAll(cfg.SearchURLTemplate, "{term}", defaultLiveTerm))
	cookies, err := parsePastedCookies(raw, domain)
	if err != nil {
		writeLiveJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.persistClearance(ctx, storeID, &scrape.Clearance{Cookies: cookies, UserAgent: userAgent}); err != nil {
		writeLiveJSONError(w, http.StatusInternalServerError, fmt.Sprintf("could not save clearance: %v", err))
		return
	}

	writeLiveJSON(w, map[string]any{"ok": true})
}

// parsePastedCookies accepts document.cookie's own format - "name=value;
// name2=value2" - the simplest thing a bookmarklet or the browser's own
// console can produce without asking the admin to construct JSON by hand.
func parsePastedCookies(raw, domain string) ([]scrape.Cookie, error) {
	var cookies []scrape.Cookie
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		cookies = append(cookies, scrape.Cookie{
			Name: name, Value: strings.TrimSpace(value), Domain: domain, Path: "/",
		})
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("no cookies found in that text - expected \"name=value; name2=value2\"")
	}
	return cookies, nil
}

// persistClearance saves cl as storeID's scrape_clearances row and clears
// any blocked state, common to both the live-solve and cookie-paste paths.
func (s *Server) persistClearance(ctx context.Context, storeID int64, cl *scrape.Clearance) error {
	cookiesJSON, err := json.Marshal(cl.Cookies)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.store.UpsertScrapeClearance(ctx, db.ScrapeClearance{
		StoreID:     storeID,
		CookiesJSON: string(cookiesJSON),
		UserAgent:   cl.UserAgent,
		ObtainedAt:  now,
		ExpiresAt:   now.Add(clearanceTTL),
	}); err != nil {
		return err
	}
	if err := s.store.ClearScrapeConfigBlocked(ctx, storeID); err != nil {
		return err
	}
	_ = s.store.LogEvent(ctx, db.AppEvent{
		Action:     "scrape.clearance_saved",
		TargetType: "store",
		TargetID:   fmt.Sprintf("%d", storeID),
		Status:     "ok",
	})
	return nil
}

func writeLiveJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeLiveJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
