package scrape

import (
	"context"
	"fmt"
	"log"
	"strings"

	"goeat/safefetch"
)

// browserHeaders make a request look like a real Chrome tab. Grocery sites
// serve a challenge page - or an empty shell - to clients that do not send
// them, so these are not cosmetic.
var browserHeaders = map[string]string{
	"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	"Accept-Language":           "en-US,en;q=0.9",
	"Sec-Fetch-Dest":            "document",
	"Sec-Fetch-Mode":            "navigate",
	"Sec-Fetch-Site":            "none",
	"Sec-Fetch-User":            "?1",
	"Upgrade-Insecure-Requests": "1",
}

// SmartResult is a fetch plus how it was obtained, so callers can tell the
// operator why a page came back empty.
type SmartResult struct {
	*FetchResult
	ViaProxy  bool   // served by the headless browser rather than a direct request
	Backend   string // which renderer served it ("FlareSolverr", "Browserless")
	Challenge string // non-empty when the page looked like a bot wall
}

// FetchSmart is the fetch every scraping path should use.
//
// It sends browser headers with a cookie jar (sites hand out a session cookie
// on the first hop and reject the redirect target without it), keeps the body
// on a 403/503 so a challenge page can be recognised rather than swallowed as
// "HTTP 403", and, when the page is a bot wall or a JavaScript shell, retries
// through the configured headless browser - FlareSolverr for anti-bot
// challenges, Browserless for JavaScript-rendered pages. With no renderer
// configured the challenge is reported instead of solved, so the operator
// learns why the page came back empty.
func FetchSmart(ctx context.Context, rawURL string, render RenderConfig) (*SmartResult, error) {
	return FetchSmartWithContext(ctx, rawURL, render, nil)
}

// FetchSmartWithContext is FetchSmart for a store that needs a selection made
// before its search page shows anything (see StoreContext). When a context is
// present the direct fetch is skipped entirely: it cannot carry cookies into
// the renderer, so it would only ever return the store's empty shell.
func FetchSmartWithContext(ctx context.Context, rawURL string, render RenderConfig, sc *StoreContext) (*SmartResult, error) {
	if !sc.Empty() {
		if out, ok := fetchWithStoreContext(ctx, rawURL, render, sc); ok {
			return out, nil
		}
		// Fall through: better a shell we can explain than nothing at all.
	}

	res, err := safefetch.Fetch(ctx, rawURL, &safefetch.Options{
		MaxBytes:      maxBodyBytes,
		Timeout:       fetchTimeout,
		Headers:       browserHeaders,
		CookieJar:     true,
		KeepErrorBody: true,
	})

	var direct *FetchResult
	var challenge string
	if err == nil {
		direct = &FetchResult{
			HTML:       string(res.Body),
			FinalURL:   res.FinalURL,
			StatusCode: res.StatusCode,
		}
		challenge = ChallengeReason(direct.HTML, direct.StatusCode)
		if challenge == "" {
			return &SmartResult{FetchResult: direct}, nil
		}
	}

	if !render.Enabled() {
		if err != nil {
			return nil, fmt.Errorf("%w (no headless browser configured - set one up in Settings to get past JavaScript and cookie challenges)", err)
		}
		return &SmartResult{FetchResult: direct, Challenge: challenge}, nil
	}

	// Walk every configured service, the one suited to this symptom first: a
	// bot wall is FlareSolverr's job, a JavaScript shell is Browserless's. A
	// service that fails, or that comes back still blocked, falls through to
	// the next one instead of ending the fetch.
	var notes []string
	var blocked *SmartResult

	for _, renderer := range render.Chain(challenge) {
		rendered, rerr := RenderVia(ctx, renderer, rawURL)
		if rerr != nil {
			notes = append(notes, renderer.Label()+" failed: "+rerr.Error())
			continue
		}

		again := ChallengeReason(rendered.HTML, rendered.StatusCode)

		// Browserless walled off, FlareSolverr available: FlareSolverr can
		// walk through the front door and collect the clearance cookie, and
		// Browserless can then render the page - JavaScript and all - as an
		// already-vetted visitor. Neither service can do that alone.
		if again != "" && renderer.Backend == RendererBrowserless {
			if with, label, ok := fetchWithClearance(ctx, render, renderer, rawURL); ok {
				return &SmartResult{FetchResult: with, ViaProxy: true, Backend: label}, nil
			}
			notes = append(notes, "clearance handoff did not get past "+again)
		}

		if again == "" {
			return &SmartResult{FetchResult: rendered, ViaProxy: true, Backend: renderer.Label()}, nil
		}
		notes = append(notes, renderer.Label()+" returned a challenge: "+again)
		blocked = &SmartResult{
			FetchResult: rendered,
			ViaProxy:    true,
			Backend:     renderer.Label(),
			Challenge:   again,
		}
	}

	// Nothing got through. Prefer a rendered challenge page over the direct
	// one - it is the further-along attempt and more useful to look at.
	detail := strings.Join(notes, "; ")
	if blocked != nil {
		blocked.Challenge = blocked.Challenge + " (" + detail + ")"
		return blocked, nil
	}
	if direct != nil {
		return &SmartResult{FetchResult: direct, Challenge: challenge + " (" + detail + ")"}, nil
	}
	return nil, fmt.Errorf("direct fetch failed (%v) and every headless browser failed: %s", err, detail)
}

// challengeMarkers are strings that appear on bot walls and interstitials.
var challengeMarkers = []struct{ needle, reason string }{
	{"just a moment", "Cloudflare interstitial"},
	{"enable javascript and cookies to continue", "site requires JavaScript and cookies"},
	{"cf-browser-verification", "Cloudflare browser verification"},
	{"cf_chl_opt", "Cloudflare challenge"},
	{"_incapsula_resource", "Imperva/Incapsula challenge"},
	{"px-captcha", "PerimeterX challenge"},
	{"are you a human", "bot check"},
	{"access denied", "access denied"},
	{"request unsuccessful. incapsula", "Incapsula block"},
	{"pardon our interruption", "bot check"},
	{"unusual traffic from your computer", "rate-limit block"},
}

// ChallengeReason reports why a response looks like a bot wall or an empty
// JavaScript shell rather than a real page. It returns "" for a page worth
// parsing.
func ChallengeReason(html string, status int) string {
	lower := strings.ToLower(html)
	for _, m := range challengeMarkers {
		if strings.Contains(lower, m.needle) {
			return m.reason
		}
	}
	switch status {
	case 403:
		return "HTTP 403 - blocked as a bot"
	case 429:
		return "HTTP 429 - rate limited"
	case 503:
		return "HTTP 503 - challenge or throttle"
	}
	if status >= 400 {
		return fmt.Sprintf("HTTP %d", status)
	}
	// A body with almost no text is a client-rendered shell: the markup only
	// appears after JavaScript runs, which only FlareSolverr can do.
	if len(strings.TrimSpace(html)) < 2000 && !strings.Contains(lower, "<article") {
		if !strings.Contains(lower, "$") {
			return "page appears to be rendered by JavaScript (no content in the HTML)"
		}
	}
	return ""
}

// fetchWithClearance borrows cookies from FlareSolverr and retries the page
// through Browserless with them. It reports ok only when the result is a real
// page, so a caller can fall back to the plain chain otherwise.
func fetchWithClearance(ctx context.Context, cfg RenderConfig, browserless Renderer, rawURL string) (*FetchResult, string, bool) {
	var flare Renderer
	for _, r := range cfg.Renderers() {
		if r.Backend == RendererFlareSolverr {
			flare = r
			break
		}
	}
	if !flare.Enabled() {
		return nil, "", false
	}

	clearance, err := FlareClearance(ctx, flare.URL, rawURL)
	if err != nil {
		log.Printf("scrape: could not get clearance for %s: %v", rawURL, err)
		return nil, "", false
	}

	rendered, err := RenderViaWith(ctx, browserless, rawURL, clearance)
	if err != nil {
		log.Printf("scrape: cleared render of %s failed: %v", rawURL, err)
		return nil, "", false
	}
	if ChallengeReason(rendered.HTML, rendered.StatusCode) != "" {
		return nil, "", false
	}
	return rendered, "Browserless with FlareSolverr clearance", true
}

// fetchWithStoreContext runs the scripted Browserless session for a store that
// needs a context, borrowing FlareSolverr clearance first when the site is
// also behind a bot wall.
func fetchWithStoreContext(ctx context.Context, rawURL string, render RenderConfig, sc *StoreContext) (*SmartResult, bool) {
	var browserless Renderer
	for _, r := range render.Renderers() {
		if r.Backend == RendererBrowserless {
			browserless = r
			break
		}
	}
	if !browserless.Enabled() {
		// Store context is a browser feature; without one there is nothing to
		// drive and the ordinary path at least reports the challenge.
		return nil, false
	}

	attempt := func(cl *Clearance) (*SmartResult, bool) {
		rendered, err := RenderWithContext(ctx, browserless, rawURL, sc, cl)
		if err != nil {
			log.Printf("scrape: store-context render of %s failed: %v", rawURL, err)
			return nil, false
		}
		label := "Browserless (store context)"
		if cl != nil {
			label = "Browserless (store context + FlareSolverr clearance)"
		}
		if reason := ChallengeReason(rendered.HTML, rendered.StatusCode); reason != "" {
			return &SmartResult{FetchResult: rendered, ViaProxy: true, Backend: label, Challenge: reason}, false
		}
		return &SmartResult{FetchResult: rendered, ViaProxy: true, Backend: label}, true
	}

	if out, ok := attempt(nil); ok {
		return out, true
	}

	// Blocked. Same handoff as the plain chain: FlareSolverr walks in the
	// front door, Browserless replays the store context as a vetted visitor.
	var flare Renderer
	for _, r := range render.Renderers() {
		if r.Backend == RendererFlareSolverr {
			flare = r
			break
		}
	}
	if !flare.Enabled() {
		return nil, false
	}
	clearance, err := FlareClearance(ctx, flare.URL, rawURL)
	if err != nil {
		log.Printf("scrape: clearance for store-context render of %s failed: %v", rawURL, err)
		return nil, false
	}
	return attempt(clearance)
}
