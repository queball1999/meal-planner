# Phase 4 Commit Plan — Pricing Engine
Date: 2026-09-05
Spec ref: §6.0–§6.7

Phase 4 is the largest phase. It delivers: the price DB as system of record (§6.0),
the PriceProvider interface + all five fully-working adapters (§6.2), item↔product
normalization and matching (§6.3), quantity/pack-size math (§6.4), source badges (§6.6),
a priced plan with confidence summary, the shopping list, an admin manual-price page,
and the §6.7 scrape configuration tool (Assisted + Auto + Auto+AI modes).

---

## Commits

### 1. db: pricing, shopping-list, and scrape-config migration
Files: `db/migrations/00005_pricing.sql`

Tables: `price_cache`, `manual_prices`, `item_product_map`, `scrape_configs`,
`shopping_list_items`

```
db: add pricing, shopping list, and scrape config migration
```
Status: ⬜

---

### 2. db: pricing models and store interface
Files: `db/models.go`, `db/store.go`

Models: `PriceCache`, `ManualPrice`, `ItemProductMap`, `ScrapeConfig`,
`ShoppingListItem` and their Create/Upsert/Get/List param structs.

Store interface: `UpsertPriceCache`, `GetPriceCache`, `GetManualPrice`,
`UpsertItemProductMap`, `GetItemProductMap`, `CreateScrapeConfig`,
`GetScrapeConfig`, `ListScrapeConfigs`, `UpdateScrapeConfig`,
`CreateShoppingListItem`, `ListShoppingListItems`, `CheckShoppingListItem`,
`UpdatePlanTotal`.

```
db: add pricing and shopping list models and store interface
```
Status: ⬜

---

### 3. db: pricing query implementations
Files: `db/price_cache.go`, `db/manual_prices.go`, `db/item_product_map.go`,
`db/scrape_configs.go`, `db/shopping_list_items.go`

```
db: implement pricing and shopping list query methods
```
Status: ⬜

---

### 4. pricing: PriceProvider interface, normalizer, and resolution chain
Files: `pricing/provider.go`, `pricing/normalize.go`, `pricing/chain.go`

- `PriceResult` struct: `PriceCents`, `PurchaseUnit`, `PackSize`, `Source`,
  `Confidence`, `FetchedAt`
- `PriceProvider` interface: `Lookup(ctx, term, storeID, region) (*PriceResult, error)`
- `Confidence` constants: `ConfidenceLive`, `ConfidenceCached`, `ConfidenceManual`,
  `ConfidenceScrape`, `ConfidenceEstimate`
- `Normalize(raw string) string` — strip prep words, singularize, map synonyms
  ("scallion" → "green onion"); deterministic, no LLM
- `Chain.Resolve(ctx, term, storeID, region) (*PriceResult, error)` — walks
  providers in order, stops at first acceptable confidence

```
pricing: add PriceProvider interface, normalizer, and resolution chain
```
Status: ⬜

---

### 5. pricing: CacheProvider and ManualProvider adapters
Files: `pricing/cache.go`, `pricing/manual.go`

- `CacheProvider` reads `price_cache`; respects `PRICE_CACHE_TTL_HOURS` (default 168).
  Carries forward the original source's confidence — never promotes an AI estimate.
- `ManualProvider` reads `manual_prices` for the given store + region (ZIP).

```
pricing: add CacheProvider and ManualProvider adapters
```
Status: ⬜

---

### 6. pricing: AIEstimateProvider adapter
Files: `pricing/ai_estimate.go`

- Calls `llm.Generator` with a tightly structured prompt: region, store name,
  ingredient name + quantity. Expects a JSON response: `{price_cents, unit, pack_size}`.
- Always the floor of the chain. Tags result `ConfidenceEstimate`, writes to
  `price_cache` (via callback — no SQL in provider).
- Fails soft: parse errors → "no answer".

```
pricing: add AIEstimateProvider adapter
```
Status: ⬜

---

### 7. pricing: OfficialAPIProvider — Kroger adapter
Files: `pricing/kroger.go`, `config/config.go` (add `KrogerClientID`,
`KrogerClientSecret`, `KrogerLocationID`)

- OAuth2 client-credentials token fetch and refresh (in-memory, not persisted).
- Products API search: `/v1/products?filter.term=…&filter.locationId=…`.
- Picks best match by normalized name similarity. Tags `ConfidenceLive`.
- Fails soft on non-200/timeout: returns "no answer", logs warning.

```
pricing: add OfficialAPIProvider (Kroger) with OAuth2 client-credentials
```
Status: ⬜

---

### 8. pricing: ScraperProvider — configurable scraping engine
Files: `pricing/scraper.go`, `scrape/extractor.go`, `scrape/fetch.go`

- `ScraperProvider` reads the store's `ScrapeConfig` from DB, rate-limits
  (1 req/2s per store, configurable), enforces a 10s timeout.
- `scrape/fetch.go`: server-side HTTP GET with size cap (512 KB) + timeout;
  optional FlareSolverr proxy (`FLARESOLVERR_URL`).
- `scrape/extractor.go`: given HTML + `selectors_json` (CSS/XPath), extracts
  product name, price, pack size. Returns `[]ExtractedProduct`.
- JSON-LD/schema.org `Product`/`Offer` auto-detection heuristic (used by Auto mode).
- Fails soft always; degraded configs fall through to next provider.

```
pricing: add ScraperProvider with configurable CSS/XPath extraction engine
```
Status: ⬜

---

### 9. pricing: quantity/pack math and plan costing
Files: `pricing/quantity.go`, `pricing/costing.go`

- `AggregateIngredients(meals []*db.Meal, ingredients []*db.MealIngredient) []AggItem`
  — sums identical normalized terms across all meals in the plan.
- `PacksNeeded(need, packSize float64) int` — `ceil(need / packSize)`.
- `CostPlan(ctx, store, chain, planID, householdID, stores) (totalCents int64, summary string, err error)`:
  1. Load all ingredients for the plan.
  2. For each unique normalized term × store, resolve via chain.
  3. Compute `ceil(need/packSize) × priceCents`.
  4. Write each result as a `shopping_list_item`.
  5. Write price to `price_cache` (via db callback).
  6. Compute `total_cents` + `confidence_summary` ("N% from live/cached, M% estimated").
  7. Call `UpdatePlanTotal`.

```
pricing: add quantity/pack math and plan costing call site
```
Status: ⬜

---

### 10. web: wire costing into generation, source badges, confidence summary
Files: `plan/generate.go` (add `CostPlan` call after persist),
`web/templates/plan.html` (add total + confidence summary),
`web/templates/components/price_badge.html` (🟢🔵🟡🟠 badge partial)

- `generate.go`: after `persistPlan`, call `pricing.CostPlan`; if over budget,
  log a warning (repair loop is Phase 5).
- Plan calendar page: show budget meter, total, confidence summary line.
- Price badge partial: pick icon + color by `Source`/`Confidence`.

```
web: wire plan costing into generation and display budget meter with source badges
```
Status: ⬜

---

### 11. web: shopping list page
Files: `web/routes.go` (add `GET /list`), `web/handlers_shopping.go`,
`web/templates/shopping_list.html`

- Groups `shopping_list_items` by store. Each item: display name, buy quantity +
  unit, pack size, unit price, line total, price badge, checkbox.
- Per-store subtotal + grand total vs. budget meter.
- `POST /list/{id}/check` toggles `checked` on a shopping list item (HTMX-friendly:
  returns 204, JS updates checkbox state).
- No pantry integration yet (Phase 5).

```
web: add shopping list page grouped by store with budget meter
```
Status: ⬜

---

### 12. web: admin manual-price entry page
Files: `web/routes.go` (add `GET /admin/prices`, `POST /admin/prices`,
`POST /admin/prices/{id}/delete`), `web/handlers_admin.go`,
`web/templates/admin_prices.html`

- Lists `manual_prices` for all stores; form to add/edit (store, normalized term,
  region = household ZIP, price in dollars → cents, pack size).
- Delete button per row.
- Source badge shows `Manual` with the `updated_at` date.

```
web: add admin manual-price entry page
```
Status: ⬜

---

### 13. web: scrape configuration tool — all three modes
Files: `web/routes.go` (add `/admin/scrape/*`), `web/handlers_scrape.go`,
`web/templates/scrape_config.html`, `web/templates/scrape_test.html`

Three fully-functional configuration modes (§6.7):

- **Assisted**: user enters search URL template; server fetches the sample page and
  renders it in a sandboxed `<iframe srcdoc>` (CSP: no scripts, no external
  resources). User clicks an element; JS reads `document.elementFromPoint` in the
  iframe, derives a CSS selector, sends it to `POST /admin/scrape/selector` which
  returns the extracted text for confirmation.
- **Auto**: server fetches the page, runs JSON-LD/schema.org + Open Graph heuristics,
  proposes a complete config for one-click confirmation.
- **Auto + AI**: same as Auto but unconfident fields are resolved by the LLM
  (`SCRAPE_AI_ASSIST=true`); proposed config shown for confirmation before saving.

**Test button**: `POST /admin/scrape/{id}/test` re-fetches + extracts, shows each
selector's current output. Sets config `status` to `degraded` when price is $0 or
non-numeric.

**Sandboxing (§16 open question resolved)**: `srcdoc` iframe with
`sandbox="allow-same-origin"` + CSP header `frame-src 'none'`; `<script>` tags
stripped from fetched HTML before embedding; same-origin asset proxy at
`GET /admin/scrape/proxy?url=…` (allowlisted domains only, no JS).

```
web: add scrape configuration tool with Assisted, Auto, and Auto+AI modes
```
Status: ⬜
