# Bot-Detection Probe Findings — FlareSolverr + Browserless
Date: 2026-09-06
Scope: live end-to-end probes of the scraping pipeline against the running
`browserless` and `flaresolverr` containers (both healthy, FlareSolverr v3.5.0).

This document records what was actually tested, what came back, and two
conclusions that change how we should treat the headless-browser stack.
Every result below was reproduced against the live services, not inferred.

---

## 1. What was probed

| # | Probe | Method |
|---|-------|--------|
| 1 | Browserless basic render | `POST /content` (stealth, blockAds) → example.com |
| 2 | FlareSolverr basic request | `POST /v1` `request.get` → example.com |
| 3 | Browserless scripted session | `POST /function` with the `storeContextScript` body → example.com |
| 4 | Full chain, real store | `cmd/probe` (`FetchSmart`) → albertsons.com/search?q=milk |
| 5 | Forced clearance handoff | `cmd/probe_clearance` (`FlareClearance` → `RenderViaWith`) → Albertsons |
| 6 | Clearance cookies only | `POST /content` with Imperva cookies, no UA → Albertsons |
| 7 | FlareSolverr direct on search | `request.get` → albertsons.com/search?q=milk |
| 8 | Warm session | `sessions.create` → homepage → search → `sessions.destroy`, one session |
| 9 | `userAgent` shape check | `POST /content` with `userAgent` as object vs string |

---

## 2. Results

| # | Probe | Result |
|---|-------|--------|
| 1 | Browserless `/content` | ✅ HTTP 200, real rendered HTML (560 B, 6.4 s) |
| 2 | FlareSolverr `request.get` | ✅ HTTP 200, proper `solution` envelope, 15 cookies |
| 3 | Browserless `/function` | ✅ HTTP 200, full page returned (31.5 s — script ran to completion) |
| 4 | Full chain → Albertsons | ⚠️ `backend: Browserless`, HTTP 200, 246 KB, **0 prices**, challenge **empty** |
| 5 | Forced handoff | ❌ FlareSolverr got **34 clearance cookies** (incl. `visid_incap_1980972`, `incap_ses_189_1980972`, `nlbi_1980972`, `at_check=true`), then Browserless: **HTTP 400 `"userAgent" must be object`** |
| 6 | Cookies only, no UA | ⚠️ HTTP 200, 246 KB shell, **0 prices**, no wall markers |
| 7 | FlareSolverr direct | ⚠️ HTTP 200, 254 KB shell, **0 prices**, "milk" present, no `prd-itm-prc` |
| 8 | Warm session | ⚠️ HTTP 200, 305 KB shell, **0 prices** |
| 9 | `userAgent` as object | ✅ HTTP 200 (no validation error) |

---

## 3. Finding A — the clearance handoff has a confirmed bug (affects ALL stores)

**Symptom:** every clearance handoff dies with
`browserless: HTTP 400: POST Body validation failed: "userAgent" must be object`.

**Root cause:** `scrape/render.go` (`fetchViaBrowserless`) sends the UA as a
bare string:

```go
if cl != nil && len(cl.Cookies) > 0 {
    payload["cookies"] = cl.Cookies
    if cl.UserAgent != "" {
        payload["userAgent"] = cl.UserAgent   // ← string; this build wants an object
    }
}
```

The running `ghcr.io/browserless/chromium:latest` validates `userAgent` as an
**object**. Probe 9 confirmed the accepted shape:

```json
"userAgent": { "userAgent": "Mozilla/5.0 ..." }
```

**Impact:** FlareSolverr *always* returns a UA, so the handoff — the one
technique that can both clear a wall and run the page's JS — **fails 100% of
the time it runs, on every store**, not just Albertsons. It has never worked
against this Browserless build.

**Fix (one line):** wrap the UA in the object shape. Note the shape is
version-dependent — older Browserless accepted a string — so the robust fix is
to send the object form (accepted by current builds) and, if a 400 on
`userAgent` is still seen, retry once with the string form.

**Status:** ✅ applied (`scrape/render.go`, `fetchViaBrowserless`) and
re-verified live 2026-09-06 via `cmd/probe_clearance` against Albertsons: step
2 now returns HTTP 200 (was `400 "userAgent" must be object`). The response is
still the empty shell (0 prices) — expected per Finding B, not a regression.

---

## 4. Finding B — Albertsons is blocked at the API level; no tool combination reaches it

The block is **not** on the document. It is on the XHR that fetches products.
Evidence, across every path tried:

- The search page returns **HTTP 200** with a full UI shell (246–305 KB):
  "Filters", "Sort by Best Match", `add to cart`, `sign in` all present.
- **Zero** price-like strings (`$X.XX`) in every response.
- **Zero** product markers: no `prd-itm-prc`, no `product-comp-v1`.
- **No** wall markers: no "just a moment", no "incapsula", no "access denied".
- The 71 `data-qa` attributes present are all `ftr-*` (footer) and `prmpt-*`
  (modal) — i.e. the chrome of the page, not its content.

This held for:

- direct fetch (probe 4),
- Browserless with clearance cookies (probe 6),
- **FlareSolverr itself** — the browser that genuinely clears the Imperva wall
  (probe 7),
- a warm session that visits the homepage first, then the search, in one
  session (probe 8).

**Conclusion:** Imperva soft-blocks the session — it lets the document shell
load but starves the `pgmsearch` API call. Because the response is a clean
200 with no wall markers, `ChallengeReason` returns `""`, and the pipeline
treats the empty shell as a **success** (probe 4: `challenge:` empty). The
clearance handoff is therefore never even attempted on this store.

This matches the earlier diagnosis: the store is not gated on anything a
config field (cookie, pre-warm URL, wait selector) can supply.

**Recommendation:** stop pursuing Albertsons (and, by extension, Sam's Club /
PerimeterX) for scraping. The cost of continued effort exceeds the value; the
block is architectural, not a missing credential.

---

## 5. Secondary issue — "loaded but empty" is reported as success

Independent of Finding B, the pipeline has a general gap: a page that loads
with HTTP 200 but contains **no products** is returned as a win, because
`ChallengeReason` only fires on explicit walls (marker strings, 403/429/503).

In `scrape/fetch_smart.go` (`fetchWithStoreContext` and the plain loop in
`FetchSmartWithContext`), the success check is:

```go
if reason := ChallengeReason(rendered.HTML, rendered.StatusCode); reason != "" {
    return &SmartResult{...Challenge: reason}, false
}
return &SmartResult{...}, true   // ← ok=true even when the page has ZERO products
```

The `storeContextScript` already knows whether its price-wait resolved — it
just throws that signal away (the `catch` swallows the timeout).

**Fix as applied (sentinel-error approach, not a `ContentReady` field on
`FetchResult`):**

1. `storeContextScript` (`scrape/storecontext.go`) sets a `contentReady` flag
   from whether the price-wait resolved; returns it in `data`.
2. `RenderWithContext` parses it; when false, returns
   `nil, &emptyRenderError{result: res}` — a new error type in the same file
   that carries the rendered shell via `Result()`.
3. `fetchWithStoreContext` (`scrape/fetch_smart.go`) uses
   `errors.As(err, &emptyRenderError)` to recognize this case, returns the
   shell with `Challenge: "loaded but no products rendered"` and `ok=false`,
   which makes the caller fall through to the FlareSolverr clearance handoff
   instead of settling for the empty shell.

This turns "silent give-up" into "we actually tried the vetted-visitor
replay," which is what lets us *learn* whether a store is API-blocked or just
needs the handoff.

**Scope note:** this fix covers the store-context path only. The plain
`FetchSmartWithContext` loop (used by stores without a `StoreContext`, and by
recipe scraping in `web/handlers_scrape.go`) has no equivalent readiness
signal — `/content` doesn't report one — and was deliberately left as-is. A
"no price-like text" heuristic there would misfire on recipe pages, which
legitimately have no prices. Revisit only if a non-context store shows the
same soft-block symptom as Albertsons.

**Status:** ✅ applied (store-context path) and verified by
`go test ./scrape/ ./pricing/` (existing `storecontext_test.go`,
`fetch_smart_test.go`) 2026-09-06.

---

## 6. Bottom line

- **The infrastructure works.** Browserless (both `/content` and `/function`)
  and FlareSolverr are healthy and return real rendered HTML. The primitives
  are sound.
- **The handoff was broken** by a one-line API-shape bug (Finding A) — ✅
  fixed and re-verified live.
- **Albertsons is a dead end** at the API level (Finding B). Every path,
  including the vetted browser itself, returns the empty shell. Stop pursuing
  it.
- **"Empty shell = success"** masked both of the above on the store-context
  path (Finding 5) — ✅ fixed: `contentReady` now triggers the clearance
  handoff instead of settling. The plain-loop path (no store context) is
  unchanged by design; see the scope note in Finding 5.

## 7. Artifacts

- `cmd/probe/main.go` — full `FetchSmart` chain against a real URL (pre-existing).
- `cmd/probe_clearance/main.go` — forces the FlareSolverr → Browserless
  clearance handoff, bypassing the `ChallengeReason` gate (new, added 2026-09-06).
  Useful for diagnosing whether a store is API-blocked vs. just needs the handoff.
