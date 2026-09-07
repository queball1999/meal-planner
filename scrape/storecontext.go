package scrape

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// StoreContext is what a retailer needs to know before its search page will
// show a price at all.
//
// Albertsons and Safeway are the motivating case: their search page answers a
// perfectly good request with skeleton loaders forever until a store has been
// selected, and that selection lives in a cookie rather than in the URL. No
// selector can fix that - the products are simply not on the page. Cookies
// puts the selection back, Prewarm visits the pages that hand out the rest of
// the session, and WaitFor holds until the products have actually painted.
type StoreContext struct {
	Cookies map[string]string `json:"cookies,omitempty"`
	Prewarm []string          `json:"prewarm,omitempty"`
	WaitFor string            `json:"wait_for,omitempty"`
}

// Empty reports whether there is nothing to apply, in which case the ordinary
// single-navigation render is used.
func (c *StoreContext) Empty() bool {
	return c == nil || (len(c.Cookies) == 0 && len(c.Prewarm) == 0 && c.WaitFor == "")
}

// ParseStoreContext reads a scrape config's context_json. A blank or malformed
// value yields an empty context rather than an error: a bad hand-edit should
// degrade to "no store context", not break the store's pricing entirely.
func ParseStoreContext(raw string) *StoreContext {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return &StoreContext{}
	}
	var c StoreContext
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return &StoreContext{}
	}
	return &c
}

// RenderWithContext drives one Browserless session that sets the store's
// cookies, walks its pre-warm URLs, then loads the target and waits for the
// products. It needs /function (a scripted session) rather than /content,
// which can only ever perform a single navigation.
func RenderWithContext(ctx context.Context, r Renderer, targetURL string, sc *StoreContext, cl *Clearance) (*FetchResult, error) {
	if r.Backend != RendererBrowserless {
		return nil, fmt.Errorf("scrape: store context needs Browserless")
	}

	// Everything the script needs travels as JSON, so no operator-supplied
	// value is ever concatenated into the JavaScript source.
	args := map[string]any{
		"url":     targetURL,
		"cookies": contextCookies(targetURL, sc, cl),
		"prewarm": sc.Prewarm,
		"waitFor": sc.WaitFor,
	}
	if cl != nil && cl.UserAgent != "" {
		args["userAgent"] = cl.UserAgent
	}
	payload, err := json.Marshal(map[string]any{"code": storeContextScript, "context": args})
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(strings.TrimSpace(r.URL), "/")
	endpoint := base + "/function?stealth=true&blockAds=true&timeout=180000"
	if r.Token != "" {
		endpoint += "&token=" + r.Token
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}

	resp, err := (&http.Client{Timeout: contextRenderTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("browserless function: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRenderBytes))
	if err != nil {
		return nil, fmt.Errorf("browserless function: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("browserless function: status %d: %s", resp.StatusCode, snippetOf(raw))
	}

	var out struct {
		Data struct {
			HTML         string `json:"html"`
			URL          string `json:"url"`
			Status       int    `json:"status"`
			Error        string `json:"error"`
			ContentReady bool   `json:"contentReady"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("browserless function: unexpected response: %s", snippetOf(raw))
	}
	if out.Data.Error != "" {
		return nil, fmt.Errorf("browserless function: %s", out.Data.Error)
	}
	status := out.Data.Status
	if status == 0 {
		status = http.StatusOK
	}
	res := &FetchResult{HTML: out.Data.HTML, FinalURL: out.Data.URL, StatusCode: status}
	// The page loaded but the wait never resolved: a full UI with no products.
	// That is the soft-block signature, not a success. Return the shell as a
	// sentinel error so the caller can escalate (clearance handoff) instead of
	// settling for the empty page. A hard failure (network, timeout, script
	// error) still returns a plain error with no shell.
	if !out.Data.ContentReady {
		return nil, &emptyRenderError{result: res}
	}
	return res, nil
}

// emptyRenderError is returned by RenderWithContext when the page loaded but
// its content wait timed out - a full UI shell with no products. It carries
// the rendered shell so the caller can still inspect it, while signalling
// "not a real result" so the chain escalates rather than accepting it.
type emptyRenderError struct {
	result *FetchResult
}

func (e *emptyRenderError) Error() string {
	return "page loaded but the content wait timed out (no products rendered)"
}

// Result returns the rendered shell carried by the error, if any.
func (e *emptyRenderError) Result() *FetchResult { return e.result }

// contextCookies merges the operator's store selection with any clearance
// cookies, in Puppeteer's setCookie shape. The operator's own values win: a
// clearance cookie is about getting in, the store cookie about what is shown.
func contextCookies(targetURL string, sc *StoreContext, cl *Clearance) []Cookie {
	var out []Cookie
	if cl != nil {
		out = append(out, cl.Cookies...)
	}

	domain := cookieDomain(targetURL)
	names := make([]string, 0, len(sc.Cookies))
	for name := range sc.Cookies {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order keeps a failing render reproducible

	for _, name := range names {
		out = append(out, Cookie{
			Name:   name,
			Value:  sc.Cookies[name],
			Domain: domain,
			Path:   "/",
		})
	}
	return out
}

func snippetOf(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// storeContextScript is the Browserless function body. It reports failures as
// data rather than throwing, so a timeout on the wait still returns whatever
// the page had - a half-painted page is more useful to look at than an error.
const storeContextScript = `
export default async ({ page, context }) => {
  const { url, cookies, prewarm, waitFor, userAgent } = context;
  try {
    if (userAgent) await page.setUserAgent(userAgent);
    if (cookies && cookies.length) await page.setCookie(...cookies);

    for (const step of (prewarm || [])) {
      try {
        await page.goto(step, { waitUntil: 'domcontentloaded', timeout: 45000 });
        await new Promise(r => setTimeout(r, 2500));
      } catch (e) { /* a pre-warm hop is best effort */ }
    }

    const resp = await page.goto(url, { waitUntil: 'networkidle2', timeout: 60000 });

    // contentReady tells the caller whether the wait actually resolved. A page
    // that loads with a full UI but never paints its products is the soft-block
    // signature (Imperva lets the shell through and starves the product API);
    // without this flag the caller cannot tell that from a genuine result.
    let contentReady = false;
    if (waitFor) {
      try { await page.waitForSelector(waitFor, { timeout: 30000 }); contentReady = true; }
      catch (e) { /* fall through: return what rendered */ }
    } else {
      try {
        await page.waitForFunction(
          () => /[$£€]\s?\d+[.,]\d{2}/.test(document.body.innerText),
          { timeout: 25000 });
        contentReady = true;
      } catch (e) { /* same */ }
    }

    return {
      data: {
        html: await page.content(),
        url: page.url(),
        status: resp ? resp.status() : 200,
        contentReady: contentReady,
      },
      type: 'application/json',
    };
  } catch (e) {
    return { data: { error: String(e && e.message ? e.message : e) }, type: 'application/json' };
  }
};
`
