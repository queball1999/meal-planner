package scrape

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Renderer backends. A renderer is an external headless-browser service the
// operator runs alongside Go Eat; the app never embeds a browser itself
// (shipping Chrome inside the binary would dwarf the app and turn every
// Chrome CVE into our release problem).
//
//   - RendererFlareSolverr solves anti-bot challenges (Cloudflare's "just a
//     moment" interstitial) with an undetected browser. Use it when a site
//     blocks you outright.
//   - RendererBrowserless is a plain headless Chrome (browserless/chromium
//     and compatible services exposing POST /content). Use it when a site
//     answers fine but renders its prices in JavaScript.
//
// Sites need both behaviours, often on the same shopping list, so a
// RenderConfig can hold both services at once (RendererAuto). Chain then
// orders them per page: a bot wall goes to FlareSolverr first, a JavaScript
// shell to Browserless first, and whichever runs first is only fallen back on
// when it fails or comes back still blocked.
const (
	RendererNone         = ""
	RendererFlareSolverr = "flaresolverr"
	RendererBrowserless  = "browserless"
	RendererAuto         = "auto" // both, ordered by what the page looks like
)

// Renderer is one headless-browser service.
type Renderer struct {
	Backend string // RendererFlareSolverr | RendererBrowserless
	URL     string // base URL, e.g. http://flaresolverr:8191 or http://browserless:3000
	Token   string // optional auth token (Browserless ?token=)
}

// Enabled reports whether this service is usable.
func (r Renderer) Enabled() bool {
	return r.Backend != RendererNone && r.Backend != RendererAuto && strings.TrimSpace(r.URL) != ""
}

// Label names the backend for UI messages.
func (r Renderer) Label() string {
	switch r.Backend {
	case RendererFlareSolverr:
		return "FlareSolverr"
	case RendererBrowserless:
		return "Browserless"
	default:
		return "none"
	}
}

// RenderConfig points at the operator's headless-browser services. Backend
// selects which are in play: one of them, or RendererAuto for both.
type RenderConfig struct {
	Backend string // RendererFlareSolverr | RendererBrowserless | RendererAuto | RendererNone
	URL     string // Browserless base URL under auto; otherwise the selected backend's URL
	Token   string // optional auth token (Browserless ?token=)

	// FlareSolverrURL is the second service under RendererAuto. It is also
	// honoured alongside a Browserless-only backend, so configuring both
	// services is enough to get both - no third setting to remember.
	FlareSolverrURL string
}

// Enabled reports whether any renderer is usable.
func (c RenderConfig) Enabled() bool { return len(c.Renderers()) > 0 }

// Renderers lists every configured service, Browserless first. Use Chain to
// order them for a specific page.
func (c RenderConfig) Renderers() []Renderer {
	var out []Renderer
	add := func(r Renderer) {
		if r.Enabled() {
			out = append(out, r)
		}
	}
	switch c.Backend {
	case RendererAuto:
		add(Renderer{Backend: RendererBrowserless, URL: c.URL, Token: c.Token})
		add(Renderer{Backend: RendererFlareSolverr, URL: c.FlareSolverrURL})
	case RendererBrowserless:
		add(Renderer{Backend: RendererBrowserless, URL: c.URL, Token: c.Token})
		// A FlareSolverr URL configured alongside is a free second attempt.
		add(Renderer{Backend: RendererFlareSolverr, URL: c.FlareSolverrURL})
	case RendererFlareSolverr:
		add(Renderer{Backend: RendererFlareSolverr, URL: c.URL})
	}
	return out
}

// Chain orders the configured services for a page whose direct fetch failed
// with the given reason. FlareSolverr exists to get past a bot wall and
// Browserless to run a page's JavaScript, so whichever matches the symptom
// goes first and the other becomes the fallback.
func (c RenderConfig) Chain(challenge string) []Renderer {
	rs := c.Renderers()
	if len(rs) < 2 {
		return rs
	}
	// Browserless leads even against a bot wall: it is the only one of the
	// two that runs the page's JavaScript, and a retail search page paints
	// its products from an XHR after load. When it is blocked, FetchSmart
	// borrows clearance from FlareSolverr and gives it another go - see
	// fetchWithClearance - which beats settling for FlareSolverr's
	// script-free copy of the page.
	first := RendererBrowserless
	if !hasBackend(rs, RendererBrowserless) {
		first = RendererFlareSolverr
	}
	out := make([]Renderer, 0, len(rs))
	for _, r := range rs {
		if r.Backend == first {
			out = append(out, r)
		}
	}
	for _, r := range rs {
		if r.Backend != first {
			out = append(out, r)
		}
	}
	return out
}

// Label names the active configuration for UI messages.
func (c RenderConfig) Label() string {
	rs := c.Renderers()
	switch len(rs) {
	case 0:
		return "none"
	case 1:
		return rs[0].Label()
	default:
		names := make([]string, 0, len(rs))
		for _, r := range rs {
			names = append(names, r.Label())
		}
		return strings.Join(names, " + ")
	}
}

// hasBackend reports whether one of the configured services is b.
func hasBackend(rs []Renderer, b string) bool {
	for _, r := range rs {
		if r.Backend == b {
			return true
		}
	}
	return false
}

// isBotWall reports whether a challenge reason describes an anti-bot
// interstitial (FlareSolverr's job) rather than a page that simply needs its
// JavaScript run (Browserless's job).
func isBotWall(reason string) bool {
	r := strings.ToLower(reason)
	if r == "" {
		return false
	}
	for _, needle := range []string{
		"cloudflare", "incapsula", "imperva", "perimeterx",
		"bot check", "access denied", "captcha", "block", "rate-limit",
	} {
		if strings.Contains(r, needle) {
			return true
		}
	}
	return false
}

const renderTimeout = 45 * time.Second

// A scripted session walks pre-warm pages before the target, so it needs more
// room than a single navigation.
const contextRenderTimeout = 200 * time.Second

// Render fetches a URL through the first configured headless browser,
// returning the HTML after the page's JavaScript has run. Callers that want
// the full fallback behaviour should use FetchSmart, which walks Chain.
func Render(ctx context.Context, cfg RenderConfig, targetURL string) (*FetchResult, error) {
	rs := cfg.Renderers()
	if len(rs) == 0 {
		return nil, fmt.Errorf("scrape: no renderer configured")
	}
	return RenderVia(ctx, rs[0], targetURL)
}

// RenderVia fetches a URL through one specific service.
func RenderVia(ctx context.Context, r Renderer, targetURL string) (*FetchResult, error) {
	return RenderViaWith(ctx, r, targetURL, nil)
}

// RenderViaWith is RenderVia carrying clearance obtained elsewhere. Only
// Browserless can use it - FlareSolverr manages its own session.
func RenderViaWith(ctx context.Context, r Renderer, targetURL string, cl *Clearance) (*FetchResult, error) {
	switch r.Backend {
	case RendererFlareSolverr:
		return FetchViaProxy(ctx, targetURL, r.URL)
	case RendererBrowserless:
		return fetchViaBrowserless(ctx, targetURL, r, cl)
	default:
		return nil, fmt.Errorf("scrape: no renderer configured")
	}
}

// fetchViaBrowserless posts to a Browserless-compatible POST /content
// endpoint. Works with browserless/chromium, CloakBrowser wrappers, and any
// service exposing the same contract: {"url": "..."} in, rendered HTML out.
func fetchViaBrowserless(ctx context.Context, targetURL string, cfg Renderer, cl *Clearance) (*FetchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")

	// stealth patches the automation tells (navigator.webdriver, the missing
	// plugin and language arrays, the headless UA) that Imperva and friends
	// fingerprint; blockAds cuts the third-party noise that keeps networkidle
	// from ever firing on a retail page. Both are ignored by builds that do
	// not know them, so an older Browserless still works.
	q := url.Values{}
	q.Set("stealth", "true")
	q.Set("blockAds", "true")
	if cfg.Token != "" {
		q.Set("token", cfg.Token)
	}
	endpoint := base + "/content?" + q.Encode()

	payload := map[string]any{
		"url": targetURL,
		// Wait for the network to settle so client-rendered prices exist by
		// the time the HTML is serialized.
		"gotoOptions": map[string]any{
			"waitUntil": "networkidle2",
			"timeout":   30000,
		},
		// A JavaScript interstitial runs its check and then reloads itself;
		// without a pause after load we serialize the challenge page rather
		// than the store.
		"waitForTimeout": 4000,
		// Retail search results arrive from an XHR after load: serializing at
		// networkidle catches the skeleton loaders, not the products. Wait
		// until a price is actually on the page. bestAttempt below means a
		// page that genuinely has no prices still comes back rather than
		// erroring.
		"waitForFunction": map[string]any{
			"fn":      `() => /[$£€]\s?\d+[.,]\d{2}/.test(document.body.innerText)`,
			"timeout": 20000,
		},
		// Return whatever rendered rather than erroring out when networkidle
		// never arrives - a page full of trackers often never goes quiet.
		"bestAttempt": true,
	}
	// Clearance from FlareSolverr, if we have it. The cookies only hold for
	// the user agent they were issued to, so both travel together.
	if cl != nil && len(cl.Cookies) > 0 {
		payload["cookies"] = cl.Cookies
		if cl.UserAgent != "" {
			payload["userAgent"] = cl.UserAgent
		}
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		// Newer Browserless builds accept a bearer token as well as ?token=.
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := (&http.Client{Timeout: renderTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("browserless: %w", err)
	}
	defer resp.Body.Close()

	html, err := io.ReadAll(io.LimitReader(resp.Body, maxRenderBytes))
	if err != nil {
		return nil, fmt.Errorf("browserless: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(html))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("browserless: HTTP %d: %s", resp.StatusCode, msg)
	}

	return &FetchResult{
		HTML:       string(html),
		FinalURL:   targetURL,
		StatusCode: resp.StatusCode,
	}, nil
}

// CheckRenderer verifies the configured service is reachable and actually
// returns rendered HTML, so the operator can test the setting instead of
// discovering it is wrong through empty shopping lists.
func CheckRenderer(ctx context.Context, cfg RenderConfig) error {
	if !cfg.Enabled() {
		return fmt.Errorf("no renderer configured")
	}
	for _, r := range cfg.Renderers() {
		if err := CheckOneRenderer(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// CheckOneRenderer verifies a single service end to end.
func CheckOneRenderer(ctx context.Context, r Renderer) error {
	res, err := RenderVia(ctx, r, "https://example.com")
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(res.HTML), "example domain") {
		return fmt.Errorf("%s responded but did not return the test page", r.Label())
	}
	return nil
}
