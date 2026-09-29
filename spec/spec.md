---
tags: [go-eat, spec, meal-planner, go]
updated: 2026-09-05
status: draft
---

# Go Eat — Specification

**Go Eat** is a self-hosted, budget-first meal planner. You give it a weekly food budget and the stores you shop at; it plans breakfast, lunch, and dinner for the week, then hands back a single consolidated shopping list with real ingredient quantities and prices that add up to (or under) your budget.

The name is a pun: it tells you to *go eat*, and it's written in **Go**.

This document is the build spec. It does not contain code. It defines scope, architecture, data model, the pricing subsystem, the AI subsystem, security posture, UI, and the phased build plan. It follows two house references:

- **`qss_security_design_v2.md`** — auth, sessions, audit, rate limiting, CSRF, threat model.
- **`qss_style_guide_v2.md`** — CSS architecture, tokens, components, layout, accessibility.

**Go Eat is Built to WCAG 2.1 AA** (style guide v2 §5); see §8.6.

Architecture patterns (OpenAI-compatible LLM client, settings read-through, server-rendered Go with `html/template`, all-SQL-in-`db`) are modeled on the **slack-llm-proxy** codebase. **UI components** — the `(?)` help tooltip, the `data-table` refresh/export toolbar, and skeleton loading — are ported from slack-llm-proxy; **search, table filters, and barcode scanning** are ported from **QInventory2.0**; **web recipe import** copies **Mealie**'s `recipe-scrapers` method (schema.org/JSON-LD → microdata → OpenGraph). **Icons** are Material Design Icons ([pictogrammers.com/library/mdi](https://pictogrammers.com/library/mdi/)), rendered inline-SVG via slack-llm-proxy's `web/icons.go` pattern (§8.4e).

---

## 1. Product Decisions (locked)

These four decisions shape everything below. They came from design review and are settled.

| Decision | Choice | Consequence |
|---|---|---|
| **Deployment** | Self-hosted, single-household | LAN-only security posture (§9). No CAPTCHA, no lockout, no bot detection by default — but the seams for them stay in place so a future public build is a config change, not a rewrite. |
| **Pricing source** | Layered with fallback | Official grocery APIs → cached/admin price DB → AI estimate. Scraping is an optional, per-store, disable-able adapter. Never the only source. (§6) |
| **Output** | All four | Consolidated shopping list + full recipes + weekly B/L/D calendar + pantry/reuse tracking. (§5) |
| **Preferences** | All four | Free-text description + structured diet/allergy toggles + per-meal effort/servings + thumbs feedback that refines future plans. (§4) |

---

## 2. Goals & Non-Goals

### 2.1 Goals

1. **Budget is the primary constraint.** Every plan targets a weekly dollar figure and shows the running total against it, live.
2. **The shopping list is the product.** Everything else (recipes, calendar) serves the goal of walking into a store (or several) and buying exactly what's needed, at prices close to reality.
3. **Prices are real, or honestly labeled when they're not.** A price is always tagged with its source and confidence (official API / cached / AI-estimated). The app never presents an estimate as if it were a scanned shelf price.
4. **Waste-aware quantities.** If a plan needs two eggs and the store sells a dozen, the list buys one dozen — once — and the pantry tracker remembers the other ten across the week. "We only need to buy 1 box of cereal."
5. **Provider-agnostic AI.** Any major provider (Anthropic, OpenAI, Google) or any OpenAI-compatible endpoint (Ollama, LM Studio, vLLM) plugs in via config.
6. **Fun, inviting UI.** Short, tasteful animations that make planning feel light, never sluggish (§8.6). Motion budget capped per the style guide.

### 2.2 Non-Goals (v1)

- **Not** an ordering/checkout integration. Go Eat produces the list; the human shops. (Deep-link to a store cart is a stretch goal, §13.)
- **Not** a nutrition/calorie tracker. Diet *tags* are honored (vegetarian, keto), but macro counting is out.
- **Not** multi-household SaaS. One household per deployment. The data model leaves room (§10.1) but the app ships single-tenant.
- **Not** a live price guarantee. Prices drift; the app timestamps every price and warns when a price is stale (§6.6).

---

## 3. Core User Flow

The happy path, first run to shopping list:

1. **Setup wizard** (first launch only): create the household account, set country + ZIP + timezone (§4.5, §16.5), set weekly budget, pick stores, describe eating habits, set diet tags & household size.
2. **Dashboard**: a calendar of meals (week or month view) plus budget/spend stats for a selectable date range (default: current week), current plan status, and a big "Plan My Week" button. (§5.5)
3. **Generate**: user hits "Plan My Week". A generation screen shows a lively progress animation while the AI drafts meals and the pricing engine costs them out.
4. **Review plan**: the weekly B/L/D calendar appears with a running budget meter. User can regenerate a single meal, swap a meal, lock meals they like, or nudge the budget.
5. **Shopping list**: user opens the consolidated list, grouped by store, with quantities, prices, per-store subtotals, and a grand total against budget. Check items off as bought.
6. **Feedback**: thumbs up/down on meals feeds §4.4's preference learning for next week.

Every step is a server-rendered page (`html/template`), progressively enhanced with small JS islands — no SPA framework, per house style.

---

## 4. Preferences (all four mechanisms)

Preferences are the input that makes a plan *yours*. All four capture methods feed a single resolved **Preference Profile** the AI reads at generation time.

### 4.1 Free-text description (per meal slot)

- Three text areas: "What do you usually eat for **breakfast** / **lunch** / **dinner**?"
- Example: *"Cereal or eggs, sometimes just toast."*
- On save, the LLM parses each blurb into structured hints: candidate foods, implied staples (milk, bread), and rough frequency. Stored as `meal_slot_hints` (§10) with the raw text kept verbatim alongside the parse, so re-parsing after a model change is possible.
- The parse drives **quantity logic**: "cereal" → one box covers many breakfasts → buy 1.

### 4.2 Structured toggles & diet tags

- **Diet**: none / vegetarian / vegan / pescatarian / keto / low-carb / gluten-free (multi-select where sensible).
- **Allergies / hard excludes**: free-tag list (peanuts, shellfish, …). These are *hard constraints* — the AI is instructed never to include them, and a post-generation validator rejects any plan that does (§7.5).
- **Dislikes**: soft avoid list (the AI tries, but may use if budget-critical, and flags it).
- **Cuisines**: preferred cuisines (Italian, Thai, Mexican, …), soft weighting.

### 4.3 Per-meal effort & servings

- **Effort slider** per meal slot: *Quick (≤15 min)* → *Standard* → *Elaborate*. Breakfast defaults Quick, dinner Standard.
- **Servings / household size**: integer, drives all quantity math. A household of 4 buys and cooks for 4. This is the *default* headcount; individual days can be overridden (§5.6).
- **Leftover tolerance**: an animated toggle — "OK to eat the same dinner twice" — which the planner uses to batch-cook, plan intentional leftovers, and cut cost. (§5.6)

### 4.4 Feedback learning

- Every generated meal gets 👍 / 👎 controls on the calendar and list.
- Feedback is stored per meal (`meal_feedback`, §10) with the meal's tags/ingredients captured at rating time.
- Next generation, the AI prompt includes a compact digest: "liked: sheet-pan chicken, oatmeal; disliked: tofu stir-fry." Purely additive weighting — never a hard filter, so one 👎 doesn't permanently ban an ingredient.
- A settings toggle lets the household clear learned feedback and start fresh.

### 4.5 Location & region

Pricing needs to know *where* the household shops. Captured in the setup wizard, before stores:

- **Country**, then **ZIP code**. In v1 country is **fixed to the United States** (the selector shows only US; the field exists so other countries are a later addition). The ZIP is the granularity everything keys on.
- The ZIP drives region resolution across the pricing chain (§6): an **official API** like Kroger resolves the ZIP to a specific store **location ID**; **manual** and **AI-estimated** prices are scoped to the ZIP's metro. Stored on the household (`zip_code`, §10) and passed as the region to every `PriceProvider`.
- Changing the ZIP later (moving) re-scopes future price lookups; existing stored prices keep their captured region so history stays honest.

### 4.6 Resolved Preference Profile

At generation time, a single resolver (one function, one place — mirrors slack-llm-proxy's `PromptResolver` discipline) composes the profile from all four sources in a fixed order: hard constraints (allergies) → diet → structured prefs → free-text hints → feedback digest. Hard constraints are never dropped; softer layers are trimmed first if the prompt gets too long.

---

## 5. Outputs (all four)

### 5.1 Consolidated shopping list (the core deliverable)

- One list for the whole week, **grouped by store**, then by aisle/category within store.
- Each line: item name, quantity to buy (in purchasable units — "1 dozen eggs", not "2 eggs"), unit price, line total, **price source badge** (§6.6), and a checkbox.
- **Per-store subtotal** and a **grand total** with a **budget meter** (green under budget, amber near, red over).
- **Pantry-aware**: items already marked "in pantry" (§5.4) are shown struck-through / collapsed, not purchased.
- Check-off state persists (you can shop over two days). Checked items stay counted in the total.
- Export: printable view + copy-to-clipboard plain text. (Cart deep-links are a stretch goal, §13.)

### 5.2 Full recipes

- Each planned meal has a recipe: title, servings, effort/time, ingredient list (scaled to household size), and numbered steps.
- Recipes are AI-generated and stored (`recipes`, §10), so viewing one costs no new AI call.
- Ingredients on a recipe link back to the shopping-list line they contributed to (traceability: "why is this on my list?").

### 5.3 Weekly meal calendar

- A 7-day × 3-slot (B/L/D) grid.
- Each cell shows the meal name, a tiny cost chip, and 👍/👎 + a "regenerate this one" + "lock" control.
- **Shared-ingredient highlighting**: hovering a meal highlights other meals that reuse the same ingredients, making the budget logic visible ("this rotisserie chicken feeds three dinners").
- **Locked meals** survive a full-week regenerate; only unlocked cells are redrawn.

### 5.4 Pantry / reuse tracking

- A **pantry list** of staples the household already owns (salt, oil, that half-used box of cereal).
- During plan costing, any ingredient matched to a pantry item is **not** added to the shopping list (or added at reduced quantity if partially depleted).
- **Cross-meal reuse within a week** is the other half: buying one 5-lb bag of rice once, consumed across four meals, is costed as a single purchase and its per-meal cost is amortized in the calendar chips.
- After shopping, "mark as stocked" moves bought staples into the pantry so next week's plan knows they're on hand.

### 5.5 Dashboard calendar & history

The dashboard is the home surface and doubles as the historical record. Plans and meals are **never deleted when a week passes** — they roll into history, so past weeks, their meals, and their real spend stay queryable.

**Calendar view**
- A calendar showing planned meals, with a **week view and a month view** (toggle between them). Week view is the B/L/D grid (§5.3); month view is a per-day summary (meal count + day's cost chip), clicking a day drills into that day's meals.
- The calendar spans **past, current, and future** weeks — scroll/page backward to review what was eaten and what it cost, forward to see planned-ahead weeks.
- Each historical meal keeps its recipe, ingredients, feedback, and the priced cost captured at the time (a snapshot, not a re-price — old weeks show what they *actually* cost, §5.5 stats).

**Stats & spend, filterable by date range**
- A stats panel shows totals for a **selectable date range**: total spend, spend vs. budget, spend per store, average cost per meal, per-slot breakdown (how much goes to breakfast vs. dinner), and most-repeated meals.
- **Every money and usage stat is filterable by date range.** A date-range picker (with quick presets: This Week, Last Week, This Month, Last Month, custom) drives the whole panel. **Default range is the current week.**
- Spend figures come from the **stored priced totals** of each week's shopping list (integer cents), so historical numbers are stable and honest — they reflect what was actually planned/bought, carrying each price's source/confidence forward (§6.6). Weeks costed partly from estimates are labeled as such in the summary.
- Week-over-week and month-over-month deltas ("$12 under last week") where a comparable prior range exists.

**Configurable week boundaries**
- A setting sets the **week start day and week end day manually** (`WEEK_START_DAY` / `WEEK_END_DAY`). **Default: Sunday → Saturday.**
- The week boundary drives the calendar's week grouping, the "current week" default filter, plan `week_start` dates, and all week-based stat rollups — defined in one place (§11.1) so calendar, planning, and stats never disagree about where a week begins.

### 5.6 Leftovers & portions

Cooking often yields more than one sitting. Go Eat plans this explicitly instead of pretending every meal is cooked fresh — which both cuts cost and reflects how people actually eat.

**Portion math**
- Every meal has a **cooked-portions** count (how many servings it yields) and a **headcount** for the day it's eaten. If portions > headcount, the surplus becomes **projected leftovers** (in servings).
- Portions come from the recipe scale; headcount defaults to household size (§4.3) but is per-day adjustable (below).

**Leftover meals**
- When a meal produces enough surplus to cover a later meal slot, the planner can **mark that later slot as "Leftovers from [meal]"** instead of generating (and shopping for) a brand-new meal. Example: Sunday's batch chili (8 portions, household of 4) covers Monday lunch — Monday lunch is auto-marked *Leftovers: Chili*, buys nothing extra, and its calendar cost chip shows **$0 new** (amortized from Sunday's cook).
- Leftover planning is gated by the **leftover-tolerance toggle** (§4.3). Off → the planner always cooks fresh; On → it batch-cooks and fills nearby slots with leftovers where portions allow.
- Leftover meals are visually distinct on the calendar (a "♻ Leftovers" badge) and link back to the source meal.
- The AI is told, at generation time, which slots are being covered by leftovers, so it doesn't plan a redundant meal there and the shopping list reflects the reduced demand.

**Per-day headcount adjustment (guests / absences)**
- Each day on the calendar has an **editable headcount** ("who's eating"), defaulting to household size. Bump Saturday dinner to 6 because guests are coming, or drop a day to 1 when the family's away.
- Changing a day's headcount **re-scales that day's portions, ingredient quantities, and cost**, and **re-computes leftovers** (more guests → less surplus → fewer downstream leftover slots; the planner may need to add a fresh meal it previously covered with leftovers).
- If a headcount change happens after a plan is generated, the affected meals are re-scaled and the shopping list + budget meter update, with a clear note of what changed. A large increase that pushes over budget offers the same swap/repair path as §7.6.

### 5.7 Recipe catalog & web import (Mealie-style), with images

AI generation (§7) is one source of meals, not the only one. Go Eat also keeps a **recipe catalog**: a growing library of real recipes the household can draw from, plan with, and re-use — and importantly, a source of **real photos** for each meal so the calendar and recipe pages look appetizing rather than text-only.

**Two ways a recipe enters the catalog**

1. **AI-generated** (§7) — saved to the catalog on generation, so a liked AI meal becomes a reusable option, not a one-off.
2. **Imported from the web by URL** — paste a recipe link, Go Eat fetches it and extracts a structured recipe into the catalog. This is where photos come from.

**How web import works — the Mealie / `recipe-scrapers` method (copied)**

Mealie imports "from hundreds of websites by simply providing a URL," built on the `recipe-scrapers` approach. Go Eat copies that method exactly:

1. **Fetch** the page server-side (bounded timeout + size cap, same guards as the price scraper §6.7).
2. **Parse structured recipe data, in priority order:**
   - **schema.org `Recipe`** in **JSON-LD** (`<script type="application/ld+json">`) — the primary and most reliable source, present on most recipe sites.
   - **Microdata / RDFa** `Recipe` markup — secondary.
   - **OpenGraph** metadata (`og:title`, `og:image`, `og:description`) — fallback for title and image when no `Recipe` block exists.
3. **Extract fields**: title, ingredient list, instructions/steps, image URL, yield/servings, prep & cook times, and nutrition/keywords where present — the same field set Mealie/`recipe-scrapers` pulls.
4. **Pull and store the image locally**: download the recipe's `image` (or `og:image`) **via `safefetch`** (§16.1), store it in the runtime `RECIPE_IMAGE_DIR` (§11.3a, served by a handler — not the embedded static tree), and reference the local copy — never hot-link the source. The original **source URL and site name are stored for attribution**.
5. **Fallback when no recipe is found**: if the page has no parseable `Recipe` block, fall back to OpenGraph title+image and drop the user into a **manual editor** pre-filled with whatever was found — the "recipe not found, fill it in yourself" path — rather than failing outright.

**Shared engine with the price scraper.** Web recipe import and §6.7's product scraping are the **same JSON-LD / microdata / OpenGraph extraction engine** pointed at different schema types (`Recipe` vs `Product`/`Offer`). It lives in **one `scrape/` package** (fetch-via-`safefetch` + extract), with two consumers — `recipes/` for `Recipe`, `pricing/` for `Product`/`Offer` (§12). This also means the same **scraper-debugger / Test** affordance applies: paste a URL, see exactly what was extracted before saving.

**Bulk & catalog management**
- **Bulk URL import**: paste several recipe URLs at once; each is fetched and added, with a per-row success/failure result (skeleton rows while they resolve, §8.4a).
- The catalog is a browsable, searchable, **`data-table` grid** (refresh + export, §8.4a) with a thumbnail, title, tags, source, and servings per row.
- Catalog recipes carry the same diet/allergy/effort tags as AI meals, so the planner can pick from catalog + AI alike under the same constraints.

**Optional recipe web search (stretch within v1).**
Beyond pasting a URL, the household can **search the web for recipes** matching a meal idea ("cheap sheet-pan chicken") and add results to the catalog. Implementation: a web search returns candidate recipe URLs, each imported via the method above. Gated behind a toggle and the same fetch guards; low priority relative to URL import, but the same plumbing.

**How the planner uses images**
- Every meal on the calendar (§5.3), the plan review, and the recipe page (§5.2) shows the recipe's photo when one exists (imported) — with a tasteful generated/placeholder image for AI meals that have none, so the grid always looks complete.
- Images respect the loading-skeleton pattern and lazy-load; `prefers-reduced-motion` and slow-connection friendly.

---

## 6. Pricing Subsystem (layered, with fallback)

The hardest part of the app, and the one most likely to break. Two design principles: **never depend on a single fragile source, always label what you're showing** — and **every price the app ever obtains is persisted**, so the local database is itself a growing, authoritative price source.

### 6.0 The price database is the system of record

- **Every price obtained by any method is written to the local SQLite price database** — API hits, manual entries, scraped values, and even AI estimates — each tagged with `source`, `confidence`, `fetched_at`, store, region, pack size, and the raw payload it came from.
- The database is not a mere cache; it is the **accumulating history** of what things cost at each store. Over time the household's own data becomes the highest-value source: real prices actually seen, not guesses.
- This makes the whole pricing layer **degrade gracefully offline** — if every live provider is unreachable, the app still costs a plan from the most recent stored prices, honestly badged with their age.
- **All five acquisition methods are fully implemented and functional — none are stubs or "interface only".** Each returns real data on a working deployment: the official API adapter makes real authenticated calls, the scraper actually fetches and extracts, the AI estimator actually queries the configured model. The chain's value depends on every link working.

### 6.1 The provider chain

A price for `(item, store, region)` is resolved by walking an ordered chain of providers until one answers with acceptable confidence:

```
1. Official API provider   (e.g., Kroger Products API)      → confidence: high
2. Cached price DB         (any previously stored price, still fresh) → confidence: carried from its original source, plus age
3. Admin/manual price DB   (operator-seeded per store)      → confidence: medium
4. Scraper adapter         (optional, per-store, off by default) → confidence: medium
5. AI price estimator      (LLM regional estimate)          → confidence: low
```

Each provider implements one interface (`PriceProvider`, §6.2). The chain is configured per store — a store with no official API skips step 1 and leans on cache/manual/AI. **Trader Joe's** (no public online prices) will typically resolve via manual DB or AI estimate; the UI is honest about that.

### 6.2 `PriceProvider` interface (conceptual)

One method: given an item description, a store, and a region, return `(priceCents, purchaseUnit, packSize, sourceTag, confidence, fetchedAt)` or "no answer". Providers are pure adapters — all persistence lives in `db` (house rule: no provider writes its own SQL; it returns a value, the pricing service caches it).

Providers, all satisfying the same interface:

- **`OfficialAPIProvider`** — wraps a real retailer API. **Kroger** ships first (documented public Products API with OAuth2 client-credentials; covers Kroger-family incl. some regional banners). Config: client ID/secret, location ID. Others (Walmart, Instacart Developer, Sam's Club) added as adapters as access allows.
- **`CacheProvider`** — reads `price_cache` for a fresh hit. Freshness window configurable (`PRICE_CACHE_TTL_HOURS`, default 168 = 1 week).
- **`ManualProvider`** — reads `manual_prices`, operator-maintained per store/region via an admin page.
- **`ScraperProvider`** — a **fully-featured, configurable scraping engine** (not a stub), driven by per-store scrape configurations the household sets up through a visual tool (§6.7). Each store's scraper is behind its own enable toggle (`SCRAPER_<STORE>_ENABLED`, default false). Rate-limited, timeout-bounded, and **fails soft** (returns "no answer", never an error that breaks the plan), and — like every method — persists whatever it successfully extracts to the price DB (§6.0).
- **`AIEstimateProvider`** — asks the configured LLM for a typical current price for the item in the household's region. Always available as the floor of the chain, so a plan can always be costed even with zero integrations. Clearly the lowest confidence.

### 6.3 Item ↔ product matching

The AI generates *ingredients* ("boneless chicken thighs, 2 lb"); providers price *products* ("Simple Truth Chicken Thighs, 5 lb, $X"). A matching layer bridges them:

- Normalize the ingredient (strip prep words, singularize, map synonyms — "scallion" ≈ "green onion").
- Query the provider with the normalized term; take the best-matching purchasable product.
- Record the chosen product and its pack size on the shopping-list line, so quantity math uses real pack sizes (the "1 dozen, not 2 eggs" rule).
- Matches are cached (`item_product_map`) so the same ingredient resolves consistently within and across weeks.

### 6.4 Quantity & pack-size math

- Plan needs *N units* of an ingredient across the week (summed over all meals, minus pantry stock).
- Provider gives *pack size P* and *pack price*.
- Buy `ceil(N / P)` packs. Leftover (`packs·P − N`) is recorded as **projected surplus** and offered to the pantry tracker after shopping.
- This is where the budget savings live: reuse and pack-rounding are surfaced, not hidden.

### 6.5 Caching & refresh

- **Every obtained price is written to `price_cache`** — API, scraper, **and** AI estimate alike — each with `fetched_at`, `source`, and `confidence` (the honest system-of-record, §6.0). `price_cache` is really the observed-price store, not just a cache; the name is kept for familiarity. (Operator-entered prices live in `manual_prices`, their own table.)
- **A cache hit carries forward the original source's confidence** — a stored AI estimate is served as low-confidence "estimated", never promoted to "live"/high just because it came from the DB. So the `CacheProvider` (chain step 2) can legitimately answer, but the chain still prefers a fresh higher-confidence provider when one is reachable (§6.1 resolves to *acceptable* confidence, not merely the first non-empty answer).
- A background refresh job (not in the request path — mirrors slack-llm-proxy's out-of-band job discipline) can pre-warm prices for a household's frequent items.
- Cache never serves an entry older than its TTL; stale entries fall through to the next live provider.

### 6.6 Honesty: source badges & staleness

Every price shown carries a small badge:

| Badge | Meaning |
|---|---|
| 🟢 **Live** | Fetched from an official API this session. |
| 🔵 **Cached (Nd)** | From cache, N days old. |
| 🟡 **Manual** | Operator-entered; date shown. |
| 🟠 **Estimated** | AI guess; "approximate" label. |

The grand total shows a **confidence summary** ("$142.10 — 78% from live/cached prices, 22% estimated"). No estimate is ever styled to look like a scanned shelf price. This is the pricing analog of the security doc's "never render 'unknown' as 'zero'" rule.

### 6.7 Scrape configuration tool

Retailer sites differ and change; a hardcoded scraper per store rots fast. Instead, Go Eat ships a **scrape configuration tool** that lets the household (or the operator) teach the app how to read a given store's site, and stores that configuration as data — so adapting to a site change is re-teaching, not recompiling.

A **scrape config** (`scrape_configs`, §10) is per-store and holds: the product-search URL template, and a set of **field selectors** (product name, price, pack size, availability) expressed as CSS/XPath selectors plus optional post-extraction regex/normalization. The `ScraperProvider` reads this config to fetch and extract; it has no store-specific logic baked in.

Three ways to build a config, all fully functional:

**1. Manual, visual point-and-click (`Assisted` mode).**
The operator enters a sample product-search URL. The tool fetches that page server-side and renders a **selector picker**: a preview of the fetched page (sanitized/sandboxed) where the operator clicks the price, then the product name, then the pack size. Each click resolves to a candidate selector, shown with the value it currently extracts so the operator confirms "yes, that's the price." This is the "with user help" flow — the human disambiguates which element on a busy page is the real price. Saved as a `scrape_config`.

**2. Automatic detection (`Auto` mode).**
The tool fetches the sample page and runs heuristic detectors: schema.org / JSON-LD `Product`/`Offer` microdata (the most reliable, present on many retail sites), Open Graph price tags, and common price-pattern matching (currency-prefixed numbers near an add-to-cart control). If it finds a confident structured match, it proposes a complete config for the operator to confirm with one click. Falls back to inviting the operator into Assisted mode when heuristics are uncertain.

**3. LLM-assisted auto-detection (`Auto + AI` — a toggle).**
When enabled (a per-store animated toggle, `SCRAPE_AI_ASSIST_ENABLED`), Auto mode additionally sends the fetched page's structure (a trimmed DOM / relevant HTML slice, **never** the operator's secrets) to the configured LLM and asks it to identify the selectors for name, price, and pack size. The model's proposal is treated as a *suggestion*: the tool extracts using the proposed selectors and shows the operator the resulting values to confirm before saving. The LLM never writes a config directly — a human always confirms the extracted values, same discipline as §7.5's "a proposal is priced/validated, not trusted."

**Validation & health.** Every saved config has a **"Test" button** that re-fetches and shows exactly what each selector currently extracts, so the operator can spot a broken selector before a plan silently mis-prices. A config that starts returning empty/implausible values (e.g., a price of $0 or non-numeric) marks itself **degraded** and the store falls through to the next provider in the chain, surfaced on the pricing admin page — never a silent bad price.

**Safety.** Scraping is off by default, per-store opt-in, rate-limited, timeout-bounded, robots-aware where applicable, and documented as ToS-sensitive and best-effort. The fetch runs server-side with a bounded timeout and size cap; the rendered preview in Assisted mode is sandboxed (no third-party script execution). This is the app's highest-risk outbound surface (§9.5) and is treated accordingly.

### 6.8 Cloudflare-protected sites: optional FlareSolverr (off by default)

Some retailer and recipe sites sit behind Cloudflare (or DDoS-Guard) challenges that a plain HTTP fetch can't pass — it gets a `403`/`503` challenge page instead of content. Because Go Eat only fetches such pages **occasionally** (a recipe import, an infrequent price scrape), an optional **[FlareSolverr](https://github.com/FlareSolverr/FlareSolverr)** integration is enough to handle them.

- **What it is**: FlareSolverr is a small companion service (run separately, typically its own Docker container) that solves the challenge with a headless browser and returns the resolved HTML + cookies. Go Eat POSTs `{"cmd":"request.get","url":...,"maxTimeout":...}` to its `/v1` endpoint and uses the returned HTML in place of a direct fetch.
- **Optional and disabled by default** (`FLARESOLVERR_ENABLED`, default false, animated toggle). With it off, a challenge-blocked fetch just fails soft the normal way (scraper returns "no answer"; recipe import falls to the manual editor, §5.7) — nothing about the base app depends on it.
- **When it's used**: only as a **fallback** — the `scrape/` engine tries a normal `safefetch` first, and only routes through FlareSolverr when it detects a Cloudflare/DDoS-Guard challenge **and** the integration is enabled. Applies to both recipe import and product scraping (they share the engine, §5.7).
- **SSRF still enforced first** (§16.1): the target URL is IP-checked by `safefetch`'s rules **before** it's handed to FlareSolverr — because FlareSolverr fetches from its *own* network context and would otherwise bypass the private-IP guard. The FlareSolverr endpoint URL itself is operator-configured and trusted. Requests to it are bounded by `FLARESOLVERR_MAX_TIMEOUT_MS`.
- **Config** (§11.3): `FLARESOLVERR_ENABLED`, `FLARESOLVERR_URL` (e.g. `http://localhost:8191/v1`), `FLARESOLVERR_MAX_TIMEOUT_MS` (default 60000). Documented in the operator guide as an optional add-on with its own container; not required for v1.

---

## 7. AI Subsystem (provider-agnostic)

Meal generation needs an LLM. The design mirrors slack-llm-proxy's provider abstraction so any backend plugs in.

### 7.1 OpenAI-compatible client

- The OpenAI-compatible `llm.Client` speaks the OpenAI Chat Completions wire format and backs three of the four provider profiles (§7.2). Config: `LLM_API_URL`, `LLM_MODEL`, `LLM_API_KEY`, `LLM_MAX_TOKENS`, `LLM_TEMPERATURE`. (The `anthropic` profile uses the native SDK instead — §7.2.)
- Every config value is read through a **settings read-through** (accessor, not a captured field), so changing the model/provider on the settings page applies to the *next* generation, not the next restart. (Directly modeled on slack-llm-proxy's `Client.WithSettings`.)

### 7.2 Provider profiles

A `PROVIDER` setting selects a profile. Because two wire formats are in play, the `llm` package defines a small **`Generator` interface** (one method: given a system prompt + messages + a JSON schema, return structured meal JSON) with **two implementations** behind it; the generation service (§7.3) never knows which is active:

- **`anthropic`** — Claude models via the **native Anthropic Go SDK** (`github.com/anthropics/anthropic-sdk-go`, the Messages API `POST /v1/messages`). Decided: Anthropic's API is **not** OpenAI-shaped, and since Go Eat is written in Go the first-party SDK is available — so this profile uses it directly rather than a fragile OpenAI-compat translation or an extra gateway the self-hoster would have to run. This unlocks Anthropic-native **structured outputs** (`output_config.format` / `messages.parse`) for §7.4's meal JSON and **adaptive thinking** (`thinking: {type:"adaptive"}`), which suit plan generation. Config: `ANTHROPIC_API_KEY`, model id (e.g. `claude-sonnet-5` — a sensible cost/quality default for a household planner; the operator may set any current model). Note: assistant **prefill is removed** on current Claude models, so response shaping is done via structured outputs/system instructions, never a prefilled turn.
- **`openai`** — OpenAI models, native OpenAI Chat Completions.
- **`google`** — Gemini via Google's OpenAI-compatible endpoint.
- **`openai_compatible`** — any base URL: Ollama, LM Studio, vLLM, LiteLLM, OpenRouter, etc. The catch-all, and the **default for self-hosters**.

The last three all speak the OpenAI wire format, so they share **one** `Generator` implementation (the §7.1 client, differing only in base URL + auth + how structured output is requested — JSON mode / response-format). Only `anthropic` needs the second implementation. Only the active profile's credentials are read. Adding an OpenAI-compatible provider is adding a profile; adding a genuinely different wire format (a future Google native SDK, say) is adding a third `Generator`, never touching the generation service.

### 7.3 The one call site

All generation goes through a single service function (analogous to slack-llm-proxy's `Auditor.RunAndLog`): it resolves the preference profile (§4.6), builds the prompt, calls the client, validates the result (§7.5), and records the run. Nothing else calls the raw client. This keeps prompt composition, validation, and logging in one place. **It runs inside an async background job** (§16.2), not a blocking request — the whole generate→price→repair pipeline is multi-second.

### 7.4 What the AI produces (structured output)

The generation prompt asks for **structured JSON**, not prose: a list of meals, each with slot, day, title, effort, servings, tagged ingredients (name + quantity + unit), and steps. Requesting structure (and validating it) is what lets pricing, the calendar, and recipes all consume one response.

**AI is one meal source of two.** The planner draws from both AI-generated meals and the **recipe catalog** (§5.7, which includes web-imported recipes with real photos). The generation prompt can be seeded with catalog candidates that fit the household's constraints, so a plan mixes fresh AI ideas with proven, image-backed recipes. Both sources carry the same tags and flow through the same pricing/validation.

- **Budget is in the prompt**, but the AI is told to plan *slightly under* and leave headroom; final costing is done by the pricing engine (§6), not trusted from the model. The model proposes meals; the app prices them.
- If the priced total exceeds budget, a **repair loop** (§7.6) asks the AI to swap the most expensive meals, up to a bounded number of iterations.

### 7.5 Validation (hard gates)

After generation, before showing anything:

1. **Allergy/exclude validator** — reject any plan containing a hard-excluded ingredient (§4.2). Regenerate that meal, don't ship it.
2. **Schema validator** — the JSON must parse into the expected shape; a malformed response is retried once, then surfaced as an error, never half-rendered.
3. **Diet consistency** — vegetarian profile + a meat ingredient = reject the offending meal.

These are the meal-planning analog of the security doc's "fail closed on the thing that matters" — a plan that violates a stated allergy is never rendered.

### 7.6 Budget repair loop

- Price the full plan (§6).
- If over budget: identify the highest-cost unlocked meals, ask the AI to replace just those with cheaper alternatives honoring the same constraints, re-price.
- Bounded to N iterations (`BUDGET_REPAIR_MAX_ITERS`, default 3). If still over after N, present the plan anyway with an honest "we got as close as we could — $X over" banner and one-tap manual swaps. Never spin forever.

### 7.7 Cost & rate awareness

- Each generation records tokens used and (if the provider has known pricing) an estimated AI cost, shown on an admin page. Reuses the "unknown ≠ zero" honesty rule for unpriced models.
- Generation is the only expensive operation; it's explicitly user-triggered (the "Plan My Week" button), never automatic, so a household controls its own AI spend.

---

## 8. UI / UX

Server-rendered `html/template` pages, QSS style guide throughout. Small JS islands for interactivity; no SPA framework, no CSS framework, hand-written CSS with design tokens.

### 8.1 Layout shell

- **Marketing/content-nav shell** (style guide §3.1): sticky 56px header (`--nav-h`), brand ("🍳 Go Eat"), nav links (Dashboard, Plan, Shopping List, Pantry, Settings), user menu top-right, slim footer with version.
- Single mobile breakpoint; nav collapses to a hamburger.
- Skip-to-content link, `.sr-only` utility, `:focus-visible` rings everywhere — all non-negotiable per style guide §5.

### 8.2 Pages

| Route | Page | Purpose |
|---|---|---|
| `/` | Dashboard | Meal calendar (week/month), date-range spend stats (default current week), plan status, big "Plan My Week" CTA (§5.5) |
| `/setup` | Setup wizard | First-run: country/ZIP/timezone, budget, stores, preferences (uses `.card--wide`) |
| `/plan` | Weekly calendar | The B/L/D grid, per-meal controls, budget meter |
| `/plan/generate` | Generation screen | Live progress (SSE) while the async job runs (§16.2) |
| `/list` | Shopping list | Grouped, priced, checkable, budget meter |
| `/pantry` | Pantry | Staples on hand, mark-as-stocked, barcode intake, filterable table |
| `/recipes` | Recipe catalog | Browsable/searchable/filterable grid of AI + imported recipes with thumbnails (§5.7) |
| `/recipes/import` | Recipe import | Paste a URL (or bulk), Mealie-style web import with a Test/preview |
| `/recipes/{id}` | Recipe detail | Full recipe for one meal |
| `/search` | Search results | Unified results across catalog, pantry, meals (§8.4c) |
| `/scan/{code}` | Scan lookup | Resolve a scanned barcode to its destination (§8.4d) |
| `/settings` | Settings | Budget, stores, AI provider, pricing providers, feedback reset |
| `/admin/prices` | Manual prices | Operator price entry per store/region (`ManualProvider` source) |
| `/auth/login` | Login | Household sign-in (§9) |

The literal `/recipes` and `/recipes/import` routes must be registered so they win over the `/recipes/{id}` wildcard (a naive `{id}` route would swallow `import` as an id — the literal-vs-wildcard precedence slack-llm-proxy's route table documents at length).

### 8.3 Design tokens

Per style guide §2, define the full palette as `:root` custom properties. Go Eat uses the **semantic-tier scheme** (recommended for new projects) with a warm, food-friendly accent instead of the default QSS blue:

- Accent: a warm green (`--accent`) evoking "fresh / go" — doubles as the pun on **Go**. Hover/bg/text/border tiers per the scheme.
- Reuse QSS danger/success/warning families for the budget meter (over = danger, near = warning, under = success) and price badges.
- Radius, shadows, spacing, type scale, `--nav-h: 56px` — all inherited from style guide §2.2–2.4.
- Dark mode via `[data-theme]` (style guide §2.3 strategy 2), since a household theme picker is a nice touch and cheap.

### 8.4 Components (reuse house patterns)

*(House-pattern references below point at **`qss_style_guide_v2.md`** sections, not this spec's §4.)*

- **Buttons** `.btn` + modifier; primary = accent, danger = destructive (clear feedback, delete plan). (style guide §4.1) **Icon-first** with MDI inline-SVG glyphs (§8.4e); icon-only variants (`.btn-icon`) carry `title`/`aria-label`.
- **Cards** for meals and setup steps.
- **Modals** (`.modal-overlay > .modal`) for meal-swap, regenerate-confirm, "why is this on my list?" — opacity/visibility toggle, never `display`. (style guide §4.3)
- **Badges/pills** for price sources, diet tags, effort level — tinted bg + matching text, never solid+white. (style guide §4.8)
- **Toasts** for "plan saved", "price refreshed"; **flash** for server-side messages. (style guide §4.9)
- **Animated toggle switches for every boolean setting, app-wide** — no plain checkboxes for on/off state anywhere (scraper enable, AI price estimates, AI scrape-assist, 2FA, dark mode, leftover tolerance, feedback learning, every security flag). The style-guide toggle (pill track + circular thumb, `translateX` on `:checked`, accent when on, `:focus-visible` ring on the track) is the single shared component; the thumb slides with a ~0.15s transition and honors `prefers-reduced-motion`. **Tag inputs** (Choices.js-style, themed to match native inputs) for allergies/cuisines. (style guide §4.2)
- **Budget meter** — a custom progress component built from tokens; the one bespoke component, styled with the success/warning/danger families.

### 8.4a Reused interaction patterns (ported from slack-llm-proxy)

Three proven components are ported verbatim in spirit from slack-llm-proxy's `web/static/js`, so Go Eat doesn't reinvent them:

- **The `(?)` help widget** (`tooltip.js`). A small `.tooltip-btn` sits beside any card title, table header, field label, or setting. Text comes from a **`data-tooltip`** attribute (falling back to `title`), so it can hold a **long, multi-sentence description** without cluttering the page. Behavior: hover previews, click locks it open (until its `×`, a second click, or a click elsewhere); popup is `position: fixed` and re-positioned at show time (so it's never clipped by a table's `overflow` scroller). `enhanceTooltips(root)` is re-runnable over content inserted after load (modals, refreshed tables). Every dense config screen (price providers, scrape tool, AI provider) uses these for the "what does this actually do?" detail.
- **The table toolbar** (`table.js`). Any table opts in with **`data-table="<name>"`** and automatically gets a **Refresh** button (re-fetches the current page, swaps just that table, and flashes it — no full reload) and an **Export ▾** menu (**CSV / JSON / YAML**, RFC-4180-quoted, filename `<name>-<date>.<ext>`). Two paging modes: client-paged (bounded sets — stores, price mappings, pantry) and server-paged (`data-server-paged`, unbounded sets — price history, audit log, meal history) with per-page navigation. Per-table URL params (`<name>_page`) so several tables can share a page.
- **Skeleton loading** (`.table-loading` / skeleton rows + `reinitTable`). Any table or panel filled by an async fetch renders **skeleton placeholder rows** first, then swaps in real rows and calls `reinitTable` so the toolbar re-reads the true row count. Used on the dashboard stats, the generation screen, price lookups, and recipe search — anywhere there's a wait, the layout is never blank or jumpy.

### 8.4b Config pages: clear step-by-step instructions

Every configuration surface — **the price-provider page especially**, plus the scrape tool, AI-provider settings, and store setup — opens with an explicit, numbered **"Getting started" instruction block** written in plain language, not just a wall of fields:

- A short intro sentence saying what this page is for.
- A **numbered step list** ("1. Pick which stores this provider covers. 2. Paste your Kroger client ID and secret. 3. Click *Test connection*. 4. Enable the provider toggle.").
- Inline `(?)` tooltips (§8.4a) on each field for the longer "why / what format / where to find it" detail.
- A **Test** action wherever a config can be verified (API credentials, a scrape selector, an LLM connection) so the user gets a green/red result before relying on it — never "save and hope."
- Empty states that teach: a page with nothing configured yet shows the steps front-and-center, not a blank table.

The price-provider page in particular walks the user through the whole chain (§6.1): which provider answers first, how to add an official API key, how to seed a manual price, and how to open the scrape tool — each as a discrete, completable step with its own Test.

### 8.4c Search & table filters (QInventory2.0-inspired)

QInventory2.0 is the UI reference for browsing/finding things; Go Eat borrows its search and filter conventions so large lists (recipe catalog, pantry, meal history, price history) stay navigable.

**Global search**
- A **header search bar** on every page (the QInventory `#header-search-input` pattern): a collapsible input that does a unified `GET /search?q=` across the recipe catalog, pantry items, and past meals, grouped by type in the results.
- **Exact-match redirect**: an exact hit (a recipe title, a barcode) jumps straight to that item; otherwise a results page renders.
- **Partial and fuzzy matching**, each a per-household **animated toggle** preference (mirrors QInventory's `search_partial_matching` / `search_fuzzy_matching` settings), with per-request URL overrides for a one-off precise search.
- The header search input doubles as a **barcode target** (§8.4d): a scanner's keystrokes or a camera scan can fill it and trigger a lookup.

**Clean table filters**
- Every data table that needs it (recipe catalog, pantry, price history, shopping-list-by-store, meal history) gets a **filter bar** above it: a text filter plus relevant faceted dropdowns (catalog: diet tag / cuisine / source; pantry: category / low-stock; price history: store / source / confidence; meal history: slot / date range).
- Filters are **applied server-side** and reflected in the URL (shareable, back-button-safe), and compose with the `data-table` toolbar (§8.4a) — filtering narrows the set, the toolbar pages and exports the filtered set (Export = the filtered rows, never just the visible page, per `table.js`).
- Filter controls follow the style guide: `.field` inputs, Choices.js multi-selects themed to match native inputs, an animated toggle for boolean facets, and a visible "clear filters" affordance. Empty results render the standard "No rows match these filters" placeholder, never a blank table.

### 8.4d Barcode scanning (QInventory2.0 tooling, where useful)

Barcodes make three flows dramatically faster; Go Eat ports QInventory2.0's scanning stack (`barcode-camera.js`, `barcode-hid.js`, server-side `Lookup`) rather than rebuilding it.

**Where it's used**
1. **Pantry intake** — scan a product to add/increment it in the pantry ("mark as stocked" by scanning the box), capturing its UPC/EAN into `pantry_items.barcode` (§10). The single highest-value use: keeping the pantry accurate is otherwise tedious typing.
2. **Product ↔ ingredient matching** — a scanned UPC resolves to a specific product, sharpening price/pack matching (§6.3) and stored in `item_product_map.barcode` for exact future lookups.
3. **In-store check-off** — while shopping, scan an item to check it off the shopping list (§5.1) and confirm the price paid.

**How it works (ported)**
- **Camera scanning**: the native **`BarcodeDetector` API** when available (Chrome/Safari), falling back to **QuaggaJS**. Colour-coded corner brackets signal state (white idle → yellow detecting → green confirmed), with torch toggle, tap-to-focus, and a two-read confirm threshold to reject misreads. Reads EAN-13/8, UPC-A/E, Code-128/39/93, QR. Requires HTTPS or localhost (a note for the deploy guide).
- **HID/USB & Bluetooth scanners**: `barcode-hid.js`'s keystroke-gap heuristic (characters arriving <60 ms apart, terminated by Enter) — a hardware scanner "just works" with no driver, filling the focused `.barcode-field` or the header search, or hitting the lookup endpoint.
- **Two scan contexts** (QInventory's model): *field* mode silently fills a form field (pantry quantity, product match); *quick* mode from the nav offers an action picker (add to pantry / look up / check off).
- **Server-side resolve**: a `barcode` package with a `Lookup(code)` that resolves a scanned UPC/EAN to a known product/pantry item (and, where an official API supports UPC lookup, to a live product+price), returning the destination the UI navigates to — the QInventory `barcode.Lookup` shape.
- **Graceful absence**: scanning is progressive enhancement. No camera and no scanner → every flow still works by typing. Camera scanning degrades to manual entry with a clear message on non-HTTPS or unsupported browsers.

### 8.4e Icons: MDI, inline SVG, icon-first controls

Go Eat uses **Material Design Icons** from **[pictogrammers.com/library/mdi](https://pictogrammers.com/library/mdi/)** for every icon, and **prefers icon buttons over text buttons** throughout. The mechanism is ported from slack-llm-proxy's `web/icons.go`.

**How icons are rendered (ported pattern)**
- A Go **`web/icons.go`** holds a curated `map[string]template.HTML` of icon name → **inline `<svg>`** (an `mdi(path)` helper wraps the 24×24 viewBox around each MDI path), exposed to templates via an **`icon` template func** — `{{icon "cart"}}` emits the SVG.
- **Inline SVG, never a webfont or `<img>`.** This is deliberate and matches both the style guide's "no web fonts" rule and a tight `default-src 'self'` CSP: an inline SVG is part of the page's own markup, not a fetched resource, so there's no vendored font file, no CDN, no external request — it works offline and ships inside the single `go:embed` binary.
- Each SVG uses **`fill="currentColor"`** at ~16–18px, so an icon inherits its button/text color automatically in light and dark themes (style guide §4.1).
- The icon set is **curated and grows on demand**: to add one, copy its path data from the MDI library page and add a named entry (with a comment citing the glyph, exactly as slack-llm-proxy does). No dynamic/all-icons bundle.

**Icon-first controls**
- **Every action control is icon-first**: refresh, export, filter, search, add, edit, delete, lock, regenerate, scan, check-off, thumbs up/down, torch, nav items, theme toggle, close/back — all render as an icon button (`.btn-icon` / `.icon-btn`), following the QInventory/slack-llm-proxy convention.
- **Accessibility is mandatory on icon-only controls** (style guide §5): every one carries a `title` **and** an `aria-label` (or `.sr-only` text). An icon with no accessible name is a bug, not a style choice.
- **Text labels are kept only where the icon alone is ambiguous or the stakes are high**: the primary CTA ("Plan My Week" keeps its words), destructive confirmations ("Delete plan"), and the setup wizard's step buttons pair an icon *with* a label. Everywhere else, icon-only with a tooltip (§8.4a) carries the meaning.
- Suggested starter glyphs (illustrative, not exhaustive): `cart`/`cart-outline` (shopping list), `silverware-fork-knife` (meals), `calendar-week`/`calendar-month` (calendar views), `fridge-outline` (pantry), `barcode-scan` (scan), `store` (stores), `chef-hat` (recipes), `refresh`, `download`/`export`, `filter-variant`, `magnify` (search), `pencil`, `delete`, `lock`/`lock-open`, `autorenew` (regenerate), `thumb-up`/`thumb-down`, `flashlight` (torch), `cog` (settings), `help-circle-outline` (the `?` tooltip trigger, §8.4a).

### 8.5 The "fun" — animations (bounded)

The user asked for fun, inviting animations. The style guide caps UI-chrome motion at ~0.25s; we honor that for chrome and reserve slightly longer, deliberate moments for the two places delight matters:

1. **Generation screen** (`/plan/generate`): a playful looping animation while the AI plans — e.g., ingredients tumbling into a pot, a progress narrative ("Drafting meals… Pricing your cart… Trimming to budget…"). This is a genuine wait (an AI call), so an engaging loader earns its place. Pure CSS/SVG, respects `prefers-reduced-motion` (falls back to a simple spinner + text).
2. **Budget meter fill**: the meter animates from 0 to the plan's total on reveal — a satisfying "did we make it?" beat. Also `prefers-reduced-motion`-aware.
3. **Micro-interactions**: meal-card lock (a small latch), check-off on the list (a gentle strikethrough sweep), 👍/👎 pop. All ≤0.2s, all optional under reduced-motion.

**Rule:** motion is never on the critical path of reading a price or a total. Fun, not friction.

### 8.6 Accessibility

Built to WCAG 2.1 AA. Full style-guide §5 baseline: the §4.11 focus bar (a square-ended 2px bottom bar in `--clr-focus`, never a rounded ring or outline; form fields get the accent border), skip link, `.sr-only`, `title`/`aria-label` on icon buttons, WCAG-AA contrast (with the before/after comment convention when tuning a dark-theme color). `prefers-reduced-motion` disables §8.5's flourishes.

---

## 9. Security (LAN single-household posture)

Deployment is self-hosted, single household → the security doc's **LAN-only reversible decisions** (§9.3) apply. We take the simpler defaults but keep every seam so a future public build flips config, not code.

### 9.1 Authentication

- **Local password auth** (security doc §1.1): `users(id, username, password_hash, …)`, **bcrypt cost 12**, never plaintext, never logged. One reusable `ValidatePassword` (min 8 chars; complexity off by default on LAN).
- Login at `/auth/login`; session token opaque, ≥32 bytes, stored server-side with TTL, set as an `HttpOnly` + `SameSite=Lax` cookie (`Secure` when served over TLS).
- Single household usually means 1–2 accounts. First account created via the setup wizard; **no implicit admin escalation** — the wizard explicitly creates the owner.
- **TOTP 2FA optional** (security doc §1.3), off by default on LAN, available as a settings toggle with backup codes. The enrollment/verify flow is specified but not required.

### 9.2 What we skip on LAN (and why it's reversible)

Per security doc §9.3, on a private household deployment these are off by default, gated behind a single flag each so a public build turns them on:

- **CAPTCHA** (`CAPTCHA_ENABLED`, default false) — pluggable provider interface present (Turnstile/reCAPTCHA/none).
- **Bot detection** (`BOT_PROTECTION_ENABLED`, default false) — UA classifier present, scoped to auth routes.

Keeping the code present-but-dormant matches the doc's intent: "public deployment decisions" become a config change.

### 9.3 Always-on, even on LAN

These are cheap and non-negotiable regardless of deployment:

- **CSRF protection** (security doc §6): `filippo.io/csrf/gorilla` cross-origin check on every non-GET request, login and setup included, no exemptions; `TrustedOrigins` from `PUBLIC_BASE_URL` with its scheme, plus a scheme guard.
- **Sign-in rate limit and lockout** (security doc §2.1–§2.2): per-IP in-memory limit (10/min) on sign-in and the password-change step-up; persistent lockout in `login_attempts`, keyed by the submitted username string — 5 failures from one (username, IP) pair or 20 from one IP in 5 minutes lock for 10 minutes; a username failing from many IPs is slowed, never hard-locked.
- **Sessions** (security doc §1.4): fixed `SESSION_TTL_HOURS` from sign-in plus a server-enforced idle limit (`SESSION_IDLE_MINUTES`, default 1440) that background polling doesn't refresh.
- **Headers** (security doc §10): nonce-based CSP (no `'unsafe-inline'` scripts, no inline event handlers), `frame-ancestors 'none'`, `X-Frame-Options`, `nosniff`, `Referrer-Policy`, `Permissions-Policy`, HSTS on https, `Cache-Control: no-store` on signed-in pages.
- **Host allowlist** (security doc §9.3): host names other than `PUBLIC_BASE_URL`'s and `ALLOWED_HOSTS` get 421 (DNS rebinding); IP literals and localhost always pass.
- **SSRF protection on all server-side fetches** (§16.1): recipe import, the scrape tool/scraper, and image downloads all go through one `safefetch` helper that blocks private/loopback/link-local IPs, pins the vetted IP, and caps redirects. On a home LAN this is the highest-value control in the app.
- **Audit logging** (security doc §5): `app_events(id, occurred_at, actor_user_id, actor_label, action, target_type, target_id, metadata, ip_address, user_agent, status)`. Log auth events, settings changes, plan generation, provider config changes. **Secrets never rendered back** — API keys logged as `(set)`, never by value (§5.3). IP extraction per §5.4: `X-Forwarded-For` is believed only from `TRUSTED_PROXIES`, read right to left; otherwise the peer address.
- **Secret handling**: all keys (AI provider keys, retailer API secrets, session secret, DB creds) live in `.env` (gitignored), never in the DB in plaintext, never in a log, never rendered back into the settings UI (write-only fields showing `(set)`/`(empty)`, per slack-llm-proxy's `Definition.Secret`).
- **Password change flow** (security doc §7.3): step-up (re-enter current), validate, hash, single-transaction update, audit, optional session invalidation.
- **Cookies**: `HttpOnly` always; `SameSite=Lax`; on an https `PUBLIC_BASE_URL` the session cookie is `Secure` with the `__Host-` prefix.
- **Session lifecycle** (security doc §1.4): fixed-at-issue expiry; optional idle-timeout warning modal with a real `/api/session/extend` POST (style guide §4.6 pattern).

### 9.4 Roles

Single household is effectively single-role (owner). The `users.role` column exists (`admin` / `read_only`) for a possible "kids can view the plan but not change the budget" future, gated by the same `RequireRole` middleware pattern (security doc §8), but v1 ships everyone as owner.

### 9.5 Threat model note

TLS is terminated at a reverse proxy if exposed beyond localhost; the app can serve HTTP on the LAN (security doc §9.3). Outbound calls to retailer APIs and AI providers use HTTPS with bounded timeouts. Scraper adapters, if enabled, are the highest-risk outbound surface — rate-limited, timeout-bounded, fail-soft, and off by default.

---

## 10. Data Model

All SQL lives in a `db` package; no handler writes its own query (house rule from slack-llm-proxy). Tables below are the v1 core. Migrations are **append-only** (never edit an applied migration).

### 10.1 Core tables

**Household & auth**
- `users(id, username, password_hash, role, totp_secret, totp_enabled, created_at, …)`
- `sessions(id, user_id, token_hash, expires_at, ip_address, user_agent, created_at)`
- `households(id, name, weekly_budget_cents, country, zip_code, region_label, timezone, household_size, created_at)` — single row in v1, but modeled as a table so multi-tenant is a later migration, not a reshape. `timezone` is an IANA name (§16.5) that all week/date math resolves in. **Location is captured at setup** (§4.6): `country` then `zip_code`, which is how pricing knows where to look up (a Kroger location, a metro for manual/AI estimates). `region_label` is a human-readable derived label. **v1 is United States only** (`country` fixed to `US`); the column exists so other countries are a later addition, not a reshape.

**Preferences**
- `stores(id, household_id, name, kind, provider_chain, enabled)` — the stores the household shops at; `provider_chain` orders which `PriceProvider`s apply. (Region isn't stored here — it's the household's `zip_code`; there's no separate `regions` table in single-household v1.)
- `preferences(id, household_id, diet_tags, cuisines, dislikes, leftover_tolerance, …)`
- `allergies(id, household_id, term)` — hard excludes, separate table so they're queryable and enforceable.
- `meal_slot_hints(id, household_id, slot, raw_text, parsed_json, effort, updated_at)` — free-text + its parse + per-slot effort.

**Plans & meals**
- `plans(id, household_id, week_start, week_end, budget_cents, total_cents, confidence_summary, status, created_at)` — retained after the week passes; `total_cents` is the stored priced total that feeds §5.5 history/stats
- `plan_days(id, plan_id, date, headcount, note)` — per-day headcount override (§5.6); defaults to household size, bumped for guests / dropped for absences; `note` records why ("guests over")
- `meals(id, plan_id, day, slot, title, effort, servings, cooked_portions, is_leftover, leftover_source_meal_id, locked, ai_run_id)` — `cooked_portions` drives leftover math; `is_leftover` + `leftover_source_meal_id` mark a slot filled by an earlier meal's surplus (§5.6)
- `recipes(id, meal_id, steps_json, servings, notes)`
- `meal_ingredients(id, meal_id, name, quantity, unit, normalized_term)`

**Pricing**
- `price_cache(id, store_id, normalized_term, price_cents, purchase_unit, pack_size, source, confidence, fetched_at)`
- `manual_prices(id, store_id, region, normalized_term, price_cents, pack_size, updated_by, updated_at)` — `region` is the ZIP/metro the price applies to (text, from the household `zip_code`), not a FK
- `item_product_map(id, store_id, normalized_term, chosen_product, pack_size, barcode, updated_at)` — `barcode` (UPC/EAN) links a scanned product to a normalized ingredient for exact price/pack matching
- `scrape_configs(id, store_id, search_url_template, selectors_json, mode, ai_assisted, status, last_tested_at, created_at)` — per-store scrape configuration built by §6.7's tool (`mode` = assisted | auto | auto_ai; `status` = active | degraded)

**Recipe catalog (§5.7)**
- `catalog_recipes(id, household_id, title, source_kind, source_url, source_site, image_path, servings, prep_minutes, cook_minutes, tags, created_at)` — `source_kind` = ai | imported | manual; `image_path` points at the locally-stored photo
- `catalog_recipe_ingredients(id, catalog_recipe_id, name, quantity, unit, normalized_term)`
- `catalog_recipe_steps(id, catalog_recipe_id, position, text)`
- `shopping_list_items(id, plan_id, store_id, meal_ingredient_refs, display_name, buy_quantity, pack_size, unit_price_cents, line_total_cents, price_source, confidence, checked, in_pantry)`

**Pantry & feedback**
- `pantry_items(id, household_id, name, normalized_term, quantity_on_hand, unit, barcode, updated_at)` — `barcode` (UPC/EAN) set when an item is scanned in (§8.4d)
- `meal_feedback(id, meal_id, household_id, rating, meal_tags_snapshot, created_at)`

**AI & settings**
- `ai_runs(id, household_id, provider, model, prompt_tokens, completion_tokens, est_cost_cents, status, created_at)`
- `settings(key, value, source, secret, updated_at)` — runtime-editable config, env-seeded once (`ON CONFLICT DO NOTHING`, per slack-llm-proxy's seed rule), never reverted on restart.
- `app_events(…)` — audit log (§9.3).

### 10.2 Conventions

- Money is stored as integer **cents**, never floats.
- Every price row carries `source` + `fetched_at` — the honesty invariant (§6.6) is enforced at the schema level.
- **Plans and meals are retained, not deleted, once a week passes** — history (§5.5) depends on it. Stats read stored priced totals so a past week always shows what it actually cost.
- **The week boundary is resolved in one place** (§11.1's `WEEK_START_DAY`/`WEEK_END_DAY`) and every date-range rollup, calendar grouping, and "current week" default reads it — no scattered week math.
- `normalized_term` is the join key between ingredients, prices, product maps, and pantry — one normalization function, one place.

---

## 11. Configuration Surface

One canonical config section (mirrors slack-llm-proxy's "§6 is the canonical config surface" rule — add a var here, reference it from the feature). Env-loaded, most runtime-editable via `/settings` through the `settings` table.

### 11.1 Core

- `APP_NAME` (default "Go Eat"), `LISTEN_ADDR`, `PUBLIC_BASE_URL`, `DATABASE_URL` (a SQLite file path in v1, e.g. `file:./data/goeat.db`), `SESSION_SECRET`.
- `WEEK_START_DAY` (default `sunday`) / `WEEK_END_DAY` (default `saturday`) — the canonical week boundary (§5.5); the single source every calendar grouping, "current week" default, and date-range stat rollup reads. Runtime-editable via `/settings`.

### 11.2 AI provider

- `PROVIDER` (`anthropic` | `openai` | `google` | `openai_compatible`)
- **OpenAI-compatible profiles** (`openai` / `google` / `openai_compatible`): `LLM_API_URL`, `LLM_MODEL`, `LLM_API_KEY` *(secret)*, `LLM_MAX_TOKENS`, `LLM_TEMPERATURE`
- **Anthropic profile**: `ANTHROPIC_API_KEY` *(secret)*, `ANTHROPIC_MODEL` (default `claude-sonnet-5`) — native SDK, no base URL needed
- `BUDGET_REPAIR_MAX_ITERS` (default 3)

### 11.3 Pricing providers

- `PRICE_CACHE_TTL_HOURS` (default 168)
- `KROGER_CLIENT_ID`, `KROGER_CLIENT_SECRET` *(secret)*, `KROGER_LOCATION_ID` — official API example
- `AI_PRICE_ESTIMATE_ENABLED` (default true — the floor of the chain)
- `SCRAPER_<STORE>_ENABLED` (default false, one per store, animated toggle)
- `SCRAPE_AI_ASSIST_ENABLED` (default false, animated toggle — §6.7 LLM-assisted auto-detection)
- `SCRAPE_FETCH_TIMEOUT_SECONDS`, `SCRAPE_MAX_BYTES`, `SCRAPE_RATE_LIMIT_PER_MIN` — the scraper's bounded-fetch guards (shared by recipe import, §5.7)
- `FLARESOLVERR_ENABLED` (default false, animated toggle), `FLARESOLVERR_URL` (e.g. `http://localhost:8191/v1`), `FLARESOLVERR_MAX_TIMEOUT_MS` (default 60000) — optional Cloudflare-challenge solver (§6.8)

### 11.3a Recipes, search & barcode

- `RECIPE_IMAGE_DIR` — a **runtime, writable** directory for downloaded recipe photos (default `./data/recipe-images`), served by a dedicated handler. **Not** under `web/static/` — that tree is `go:embed`-ed and read-only at runtime (§16.4)
- `RECIPE_WEB_SEARCH_ENABLED` (default false, animated toggle — §5.7 optional recipe web search)
- `SEARCH_PARTIAL_DEFAULT`, `SEARCH_FUZZY_DEFAULT`, `SEARCH_EXACT_REDIRECT_DEFAULT` (all default true) — household search-behavior defaults (§8.4c), per-request overridable
- `BARCODE_LOOKUP_ENABLED` (default true, animated toggle) — enable scan flows (§8.4d); camera scanning additionally needs HTTPS or localhost
- `NORMALIZE_AI_ASSIST_ENABLED` (default false, animated toggle) — LLM-assisted ingredient normalization, cached after first resolve (§16.3)

### 11.4 Security (LAN defaults)

- `LOCKOUT_ENABLED`, `RATE_LIMIT_ENABLED`, `CAPTCHA_ENABLED`, `BOT_PROTECTION_ENABLED` — all default false
- `REQUIRE_2FA` (default false)
- CAPTCHA keys, session TTL, etc., present but dormant.

Secrets are marked `secret: true` → write-only in UI, `(set)` in audit. Not runtime-editable: `SESSION_SECRET`, `DATABASE_URL`, `LISTEN_ADDR` (enforced twice — absent from registry *and* refused by name — per slack-llm-proxy's lockdown rule).

---

## 12. Suggested Package Layout

Modeled on slack-llm-proxy's layout.

| Path | Role |
|---|---|
| `config/` | `.env` loading & validation; canonical config surface |
| `db/` | SQLite store behind a `Store` interface (§12.1; Postgres a later swap), goose migration runner, all SQL, per-table query files |
| `auth/` | password, session tokens, TOTP, `ValidatePassword` |
| `middleware/` | session/role gates, CSRF, client-IP, (dormant) rate-limit & bot detection |
| `settings/` | runtime settings registry + provider snapshot; the only reader of a live setting |
| `llm/` | OpenAI-compatible client, provider profiles, preference resolver, generation service (single call site), validators, budget repair loop |
| `pricing/` | `PriceProvider` interface + adapters (official/cache/manual/scraper/AI), the resolution chain, item↔product matching, quantity math; the scraper adapter consumes `scrape/` for `Product`/`Offer` |
| `scrape/` | the **shared** fetch-and-extract engine (§5.7): JSON-LD/microdata/OpenGraph over `safefetch`, plus the §6.7 auto-detect heuristics + Test and the optional §6.8 FlareSolverr fallback; consumed by both `recipes/` and `pricing/` |
| `plan/` | plan orchestration: generate → validate → price → repair → persist |
| `recipes/` | recipe catalog + web import (consumes `scrape/` for `Recipe`), image download/store, bulk import |
| `barcode/` | `Lookup(code)` resolving a scanned UPC/EAN to a product/pantry item; ported from QInventory2.0 |
| `search/` | unified `/search` across catalog, pantry, meals; exact/partial/fuzzy matching |
| `safefetch/` | the one SSRF-guarded HTTP fetch helper (§16.1); every external fetch goes through it |
| `web/` | HTTP server, route table, `html/template` templates, handlers, static assets, `icons.go` (§8.4e) |
| `web/static/` | hand-written CSS (split per style guide §1), JS islands |
| `docs/` | this spec, an operator setup guide, a findings log |

### 12.1 Datastore choice — SQLite (decided)

**SQLite is the datastore for v1** (zero-ops, one file), and it is also the **price system of record** (§6.0) — every price ever obtained accumulates there. The `db` layer is written against an interface (mirrors slack-llm-proxy's `web.Store` discipline) so **Postgres** remains a later config swap, but v1 targets SQLite concretely: money as integer cents, `fetched_at`/`source` on every price row, and the growing local price history as a first-class source, not a cache to be evicted.

The `scrape/` package owns the fetch-and-extract engine and the §6.7 auto-detect heuristics + Test; the `pricing/` package's scraper adapter drives it from `scrape_configs` (read as data, no store-specific logic compiled in) and the scrape-config tool's UI lives in `web/`. `recipes/` uses the same `scrape/` engine for recipe import.

---

## 13. Stretch Goals (post-v1)

- **Cart deep-links**: where a retailer supports it, a "send to cart" link that pre-fills an online basket.
- **More official price adapters**: Walmart, Instacart Developer, Sam's Club as access allows.
- **Price history & trends**: chart an item's price over weeks; flag "buy now, it's cheap."
- **Multi-week / batch planning** and a running pantry that spans weeks.
- **Shared household accounts** (the multi-tenant migration the data model already anticipates).
- **Mobile-friendly in-store mode**: a big-tap, aisle-ordered checklist view for shopping.

---

## 14. Build Plan (phased)

Each phase is shippable and independently testable. Tests live next to the code; a `make check` gate (fmt + vet + test) before any phase is "done" (slack-llm-proxy discipline).

**Phase 0 — Skeleton (½–1 day)**
Repo, `config`, `db` interface + migrations runner, datastore decision (§12.1), base `html/template` shell with style-guide tokens & CSS split, health check. No features yet.

**Phase 1 — Auth & household (1–2 days)**
Local password auth, sessions, CSRF, audit logging, setup wizard creating the owner + household + budget. Security baseline (§9.3) in place.

**Phase 2 — Preferences (1–2 days)**
All four capture mechanisms (§4), the resolved Preference Profile, and the free-text parser (first AI touch point).

**Phase 3 — AI generation (2–3 days)**
OpenAI-compatible client + provider profiles (§7.1–7.2), the single generation call site, structured output, schema + allergy + diet validators (§7.5). Output: a validated, *unpriced* weekly plan + calendar UI.

**Phase 4 — Pricing engine (4–6 days)**
The SQLite price DB as system of record (§6.0), `PriceProvider` interface + **all five fully-working providers** (AI-estimate floor, cache/history, manual, official Kroger adapter, and the configurable scraper), item↔product matching, quantity/pack math, source badges. **Includes the §6.7 scrape-configuration tool**: server-side fetch, visual point-and-click selector picker (Assisted), auto-detection heuristics (Auto), the LLM-assist toggle (Auto + AI), and per-config Test/health. Output: a *priced* plan sourced from any/all methods. The scrape tool is the swing item that widens this phase.

**Phase 5 — Budget loop & outputs (3–4 days)**
Budget repair loop (§7.6), consolidated shopping list, recipes, pantry/reuse tracking, feedback capture, and **leftovers/portions** (§5.6): cooked-portions math, leftover meals, per-day headcount overrides with re-scaling. The full deliverable set (§5).

**Phase 5.5 — Dashboard, calendar & history (1–2 days)**
Week/month calendar over past/current/future plans, retained plan history, date-range-filterable spend & usage stats (default current week), configurable week boundaries (§11.1). Reads stored priced totals so history is stable.

**Phase 5.7 — Recipe catalog & web import (2–3 days)**
The shared JSON-LD/microdata/OpenGraph extraction engine (§5.7, reused by the price scraper), URL import + bulk import, local image download/store with attribution, the catalog grid, and manual-editor fallback. Meal photos start appearing across the app.

**Phase 5.8 — Search, filters & barcode (2–3 days)**
Unified `/search` with exact/partial/fuzzy toggles, clean server-side table filters across catalog/pantry/history, and the ported barcode stack (§8.4d): camera (`BarcodeDetector`→Quagga), HID scanner, `barcode.Lookup`, wired into pantry intake, product matching, and shopping check-off. Ports the `(?)` tooltip, `data-table` toolbar (refresh/export), and skeleton-loading components (§8.4a) as shared infrastructure — pull these in early if convenient, since later phases assume them.

**Phase 6 — Polish (1–2 days)**
Animations (§8.5, reduced-motion-aware), dark mode, settings page (AI + pricing provider config), admin manual-price page, accessibility pass, operator setup docs.

**Rough total: ~4.5–6 weeks** for a solo build to a usable v1. The swing variables are the pricing engine (with the scrape tool), recipe web import, and the barcode stack — the last two are largely ports from Mealie's method and QInventory2.0, which cuts their real cost.

---

## 15. Open Questions (to resolve during build)

*(All prior open questions are now resolved — recorded below for provenance.)*

**Resolved by user direction:**
- **Anthropic wire format** → the `anthropic` profile uses the **native Anthropic Go SDK** (`anthropic-sdk-go`, Messages API), behind a common `Generator` interface alongside the shared OpenAI-compatible client (§7.2). Confirmed against current Anthropic API docs: their API is not OpenAI-shaped, structured outputs + adaptive thinking are native, prefill is removed.
- **Region granularity** → captured in **household setup as country + ZIP**; ZIP is what pricing keys on (Kroger location ID; metro for manual/AI). **v1 is United States only** (§4.5, §10).
- **Selector-picker rendering** → **yes, render the site in a sandboxed preview** so the user clicks the real elements to configure scraping — confirmed as the primary Assisted-mode flow (§6.7).
- Datastore is **SQLite** and the price DB is the **system of record** (§6.0, §12.1); **all five pricing methods ship fully-featured** including the **configurable scraper** with point-and-click, auto-detect, and LLM-assist toggle (§6.7); **all boolean settings use the animated toggle** (§8.4).

**Remaining build-time details (non-blocking):**
1. Exact structured-output request shape per OpenAI-compatible backend (JSON mode vs response-format vs prompt-only) — varies by server (Ollama/LM Studio/vLLM differ); resolve per adapter in Phase 3.
2. Sandbox hardening specifics for the §6.7 preview (CSP, strip `<script>`, proxy same-origin assets) — resolve in Phase 4.

---

## 16. Engineering Decisions & Build-Agent Handoff

Decisions a builder would otherwise guess at. Each removes ambiguity; treat these as binding for v1.

### 16.1 SSRF protection (server-side fetch is a first-class security surface)

The app fetches **arbitrary user-supplied URLs server-side** in two places — recipe import (§5.7) and the scrape tool + scraper (§6.7). On a home LAN this is the sharpest risk in the app: an unguarded fetcher will reach `http://169.254.169.254` (cloud metadata), `localhost`, the router admin page, and every other host on the LAN. **One shared, mandatory `safefetch` helper** is the only way any code fetches an external URL:

- **Scheme allowlist**: `http`/`https` only. Reject `file://`, `gopher://`, `ftp://`, etc.
- **Resolve, then check, then dial**: resolve the hostname, and **reject if any resolved IP is** private (RFC 1918), loopback, link-local (`169.254.0.0/16`, `fe80::/10`), unique-local, multicast, or unspecified. Guard against DNS-rebinding by pinning the dialed IP to the one that was checked (dial the vetted IP, not a re-resolved host).
- **Redirect cap**: follow at most ~3 redirects, re-running the IP check on **every** hop (a public URL can 302 to `localhost`).
- **Bounds**: the §11.3 `SCRAPE_FETCH_TIMEOUT_SECONDS` / `SCRAPE_MAX_BYTES` guards apply here too; read with a hard byte cap, not the declared `Content-Length`.
- Applies to recipe import, the scrape-config fetch, the scraper provider, **and** downloading recipe images (§5.7). The `pricing`/`recipes` packages never call `http.Get` directly.

### 16.2 Generation is an async background job

"Plan My Week" is a multi-second AI call **plus** N pricing lookups **plus** the repair loop (§7.6) — far too long for a blocking request. It runs as a **background job** with a **status surface**, reusing slack-llm-proxy's queue/SSE discipline:

- Submitting a plan enqueues a job and redirects to the **generation screen** (§8.5), which subscribes to a status endpoint (**SSE**, with a polling fallback) reporting stages: *Drafting meals → Pricing your cart → Trimming to budget → Done*.
- One generation per household at a time (a simple in-memory single-flight is enough for single-household); a second submit joins the running job rather than starting a parallel one.
- The animated progress screen (§8.5) is driven by real stage transitions, not a fake timer. On completion the screen advances to the plan review (§3 step 4); on failure it shows the error and a retry, never a half-rendered plan.
- Regenerating a single meal (§5.3) is a smaller job on the same machinery.

### 16.3 Ingredient ↔ product normalization (the hard algorithm, made concrete)

`normalized_term` (§10.2) is the join key for pricing, pantry, and reuse, and it's the most underspecified algorithm in the spec. v1 approach, **layered, deterministic-first**:

1. **Deterministic normalizer** (one Go function, the single source): lowercase, strip prep/qualifier words ("boneless", "fresh", "chopped", "large"), singularize, trim brand names, collapse whitespace. Pure and unit-tested.
2. **Synonym map** (a small, editable data file, e.g. `scallion→green onion`, `garbanzo→chickpea`, `cilantro→coriander`): applied after the normalizer so equivalents collapse to one term.
3. **LLM assist, cached** (optional, `NORMALIZE_AI_ASSIST_ENABLED` toggle): for a term the first two steps can't confidently resolve to an existing `item_product_map`/pantry entry, ask the configured LLM once for the canonical grocery term and its likely purchase unit, then **cache the result** in `item_product_map` so it's deterministic thereafter. Never on the hot path for an already-known term.

The barcode path (§8.4d) short-circuits all of this: a scanned UPC maps directly to a product and its normalized term. **Units** are US/imperial in v1 (matching the US-only decision, §4.5); unit conversion (cups↔oz↔lb) is a small deterministic table, not an LLM call.

### 16.4 Dependencies & tooling (pinned choices)

- **SQLite driver: `modernc.org/sqlite`** (pure-Go, **CGo-free**) — non-negotiable, because development is on Windows and deployment targets Linux (per the QSS deploy note); CGo (`mattn/go-sqlite3`) breaks the clean cross-compile and the single-binary story.
- **Migrations: `goose`** (as QInventory2.0 uses), append-only, embedded via `go:embed`. Migration files are never edited once applied (§10 rule).
- **Assets embedded via `go:embed`**: templates, static CSS/JS, migrations, the MDI icon set (§8.4e), and the synonym/unit data files — so the deploy artifact is one binary (§16.7). Downloaded recipe images (§5.7) live **outside** the binary in `RECIPE_IMAGE_DIR` (mutable at runtime).
- **Anthropic: `github.com/anthropics/anthropic-sdk-go`** (§7.2). **OpenAI-compatible profiles**: a thin hand-rolled client (no heavy SDK), matching slack-llm-proxy.
- **Barcode: QuaggaJS is MIT** — fine to vendor locally (served from `/static`, not a CDN, per CSP). The native `BarcodeDetector` path needs no dependency.
- **Recipe extraction**: `recipe-scrapers` is Python — **reimplement its *method* in Go** (a JSON-LD/microdata/OpenGraph `Recipe` parser), do not attempt to call the Python lib. `schema.org`/JSON-LD parsing is standard-library `encoding/json` over `<script type="application/ld+json">` blocks.
- **Build gate**: a `make check` equivalent (`gofmt -l` clean, `go vet`, `go test ./...`) is the definition of done for each phase; tests live beside the code. LF line endings, `GOWORK=off` if a parent `go.work` exists (QSS environment notes).

### 16.5 Timezone

Week boundaries (§5.5) and "current week" are meaningless without a timezone. **Household timezone is captured at setup** (alongside country/ZIP, §4.5) as `households.timezone` (IANA name, e.g. `America/Chicago`), defaulting from the browser at wizard time. All week grouping, date-range filters, `week_start`/`week_end`, and "today" resolve in the household timezone — one helper, one place, same discipline as the week-boundary rule (§10.2).

### 16.6 Recipe image licensing (personal-use caveat)

Imported recipe photos (§5.7) are downloaded and stored locally. Because the deployment is **self-hosted single-household**, this is personal use — but the spec is explicit: images are stored **with their source URL and site attribution** (`catalog_recipes.source_url`/`source_site`), shown on the recipe page, and the feature is documented as **personal/household use only**. If a future build ever shares the catalog beyond the household (the multi-tenant stretch, §13), image redistribution becomes a copyright question to revisit — a note in the operator docs, not a v1 blocker.

### 16.7 Deployment artifact & backup

- **One statically-linked Go binary** (CGo-free, §16.4) plus its SQLite database file and the `RECIPE_IMAGE_DIR` — that's the whole deployment. All code assets are embedded (§16.4).
- Serves HTTP on the LAN (TLS terminated at a reverse proxy if exposed; camera barcode scanning needs HTTPS or localhost, §8.4d).
- **Backup story is "copy two things": the `.db` file and `RECIPE_IMAGE_DIR`.** Document a simple stop-copy-start (or SQLite online backup) in the operator guide. A Dockerfile/compose is a nice-to-have for parity with the QSS stack but not required for v1.
- **Optional companion container**: FlareSolverr (§6.8) if the household wants Cloudflare-protected sites to work — a second container the operator runs and points `FLARESOLVERR_URL` at. Off by default; the app runs fine without it.

### 16.8 Barcode scope: scan/consume only

QInventory2.0 both **generates** barcode labels and **scans** them. Go Eat only **scans/consumes** them (§8.4d) — reading a UPC/EAN off a real product for pantry intake, product matching, and check-off. **No label generation, no barcode/QR printing** in v1 (nothing in a meal planner needs a printed label). Port only `barcode-camera.js`, `barcode-hid.js`, and the server-side `Lookup`; leave QInventory's `generate.go`/`label.go` behind.
