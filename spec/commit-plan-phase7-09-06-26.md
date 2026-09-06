# Phase 7 Commit Plan — Uncommitted Polish & Infrastructure
Date: 2026-09-06
Spec ref: §5.5, §5.7, §6, §8.4c, §8.4d, §8.5, §11.3, §11.4, §16.1

This plan covers all uncommitted work since the last commit (28336d7).
88 files changed, +5,896 / -1,259 lines.

The work falls into **7 logical commits**, each self-contained and reviewable:

---

## Commits

### 1. web: add stores catalog, state-aware store picker, and context view
Files: `web/stores_catalog.go` (new), `web/scrape_context_view.go` (new),
`web/handlers_stores.go` (extend), `web/handlers_scrape.go` (extend),
`web/templates/stores.html` (rewrite), `web/templates/scrape_config.html` (extend),
`web/routes.go`

**Stores catalog** (`web/stores_catalog.go`):
- `KnownStores` slice with `KnownStore` struct (name, kind, availableIn state).
- `KnownStoreByName(name) *KnownStore` lookup.
- `StateForZIP(zip) string` — extracts two-letter state from ZIP prefix.
- Catalog entries on the stores page filter out chains not available in the
  household's state (e.g., Publix not offered to Arizona households).

**Store context view** (`web/scrape_context_view.go`):
- `parseStoreContextForm(r) (string, error)` — validates and serializes the
  store context JSON from the scrape config form.
- `renderStoreContextHTML(cfg *db.ScrapeConfig) string` — renders the context
  editor partial for the scrape config page.

**Stores page** (`web/handlers_stores.go`):
- `storesPageData` extended: `Stores []storeRow`, `Catalog []catalogEntry`,
  `HasLLM`, `KrogerReady`, `State`, `AwayCount`.
- Each store row pairs the store with its scrape config and KnownStore hint.
- Catalog chips show availability by state; already-added stores stay visible.

**Scrape config** (`web/handlers_scrape.go`):
- `parseStoreContextForm` call in save handler.
- Context JSON persisted to `scrape_configs.context_json`.

```
web: add stores catalog, state-aware picker, and scrape context view
```
Status: ⬜

---

### 2. web: add pagination package and wire it to history + admin prices
Files: `web/pagination.go` (new), `web/pagination_test.go` (new),
`web/handlers_plan.go` (extend), `web/handlers_admin.go` (extend),
`web/templates/history.html` (extend), `web/templates/admin_prices.html` (extend)

**Pagination package** (`web/pagination.go`):
```go
type Pagination struct {
    Page       int // current page (1-based)
    TotalPages int
    TotalItems int
    HasPrev    bool
    HasNext    bool
    PrevPage   int
    NextPage   int
}

func paginate(r *http.Request, items []T) ([]T, Pagination)
func paginateNamed(r *http.Request, items []T, namePrefix string) ([]T, Pagination)
```
- `page` query param → page number (default 1, clamped).
- `paginateNamed` uses `{namePrefix}_<id>` query params so multiple tables
  on one page (admin prices) don't interfere with each other.

**History page** (`web/handlers_plan.go`):
- Plans list paginated via `paginate(r, plans)`.
- `historyPageData` gains `Page Pagination`.

**Admin prices** (`web/handlers_admin.go`):
- Each store's price list paginated via `paginateNamed(r, priceRows, fmt.Sprintf("page_%d", gs.ID))`.
- `storeWithPrices` gains `Page Pagination`.

```
web: add pagination package and wire to history + admin prices
```
Status: ⬜

---

### 3. web: add AI logos, template partials, and settings registry
Files: `web/ai_logos.go` (new), `settings/registry.go` (new),
`settings/render.go` (new),
`web/templates/partials/` (new directory — partial templates),
`web/handlers_settings.go` (extend),
`web/templates/settings.html` (extend),
`web/templates/scrape_config.html` (extend)

**AI logos** (`web/ai_logos.go`):
- Map of provider name → inline SVG logo for the AI provider selector.

**Settings registry** (`settings/registry.go`):
- Central registry for settings keys, validation, and defaults.
- `Register(key string, def SettingDef)` — register a settings key.
- `Get(key string) (interface{}, error)` — get with default fallback.
- `Validate(key string, value string) error` — validate before save.

**Settings render** (`settings/render.go`):
- `RenderSettingInput(key, value, label string) string` — renders a settings
  input field with label, help text, and validation.

**Template partials** (`web/templates/partials/`):
- Reusable partials: `pagination.html`, `toast.html`, `icon.html`,
  `setting_input.html`, `store_context_editor.html`.

**Settings page** (`web/handlers_settings.go`, `web/templates/settings.html`):
- AI provider section with logo + connectivity status.
- Pricing section with week-start toggle + cache TTL display.
- Scraper section with per-store config links.
- Household section with preferences link.

**Scrape config** (`web/templates/scrape_config.html`):
- Store context editor partial.
- Selector validation feedback.
- AI-assisted toggle.

```
web: add AI logos, settings registry, template partials, and settings page overhaul
```
Status: ⬜

---

### 4. web: upgrade toast notifications, dark mode icons, and autosave
Files: `web/static/js/main.js` (extend), `web/static/css/wizard.css` (extend),
`web/templates/layout.html` (extend)

**Toast system** (`web/static/js/main.js`):
- Universal `window.showToast(message, type, duration)` — covers every status
  in the app (autosave, AI test, validation errors, etc.).
- Icon support: success (check), info (information), warning (alert), error (close).
- Auto-dismiss timer (default 4 s, 0 = manual close).
- `role="status"` + `aria-live="polite"` on container, `aria-atomic="true"`.
- Close button on each toast.

**Dark mode icons** (`web/static/js/main.js`):
- Replace emoji sun/moon with inline SVG icons from `ICONS` map.
- Icons: sun, moon, check, close, information, alert, eye, eye-off.

**Wizard CSS** (`web/static/css/wizard.css`):
- Toast styles.
- Wizard progress bar.
- Store picker chips.
- Checkbox chips.
- Review grid.
- LLM log page styles.

**Layout** (`web/templates/layout.html`):
- Link wizard.css.
- Toast container in layout.

```
web: upgrade toast notifications, dark mode SVG icons, and autosave
```
Status: ⬜

---

### 5. web: add LLM debug log page and plan generation improvements
Files: `web/handlers_settings.go` (extend), `web/templates/llm_log.html` (new),
`web/handlers_plan.go` (extend), `web/templates/plan_generate.html` (extend),
`plan/generate.go` (extend), `llm/debug.go` (extend),
`web/routes.go`

**LLM debug log** (`web/handlers_settings.go`, `web/templates/llm_log.html`):
- `GET /admin/llm-log` — paginated AI call log with search + status filter.
- Trims to 10 most recent entries.
- Shows: timestamp, model, status (200/4xx/5xx), latency, request/response preview.

**Plan generation** (`web/handlers_plan.go`, `web/templates/plan_generate.html`):
- `planPageData` gains `HasLLM` bool.
- Plan status "error" treated same as no-plan: flash message directing user
  to AI Logs, then regenerate.
- `handlePlanGenerate` — SSE progress endpoint for the generation screen.
- `plan_generate.html` — progress animation + status messages.

**Plan generation fix** (`plan/generate.go`):
- Strip markdown code fences from LLM JSON response before unmarshal.
- Fixes "empty plan" when model wraps output in ```json ... ```.

**LLM debug** (`llm/debug.go`):
- Log request/response for debugging.

```
web: add LLM debug log page, plan generation progress screen, and markdown fence fix
```
Status: ⬜

---

### 6. misc: migrate flash → notify, fix encoding, update configs
Files: `web/handlers.go` (extend), `web/helpers.go` (extend),
`web/handlers_admin.go` (extend), `web/handlers_meals.go` (extend),
`web/handlers_plan.go` (extend), `web/handlers_preferences.go` (extend),
`web/handlers_shopping.go` (extend),
`web/static/css/components.css` (extend),
`.env.example` (extend), `Makefile` (extend), `README.md` (extend),
`docker-compose.yml` (extend), `go.mod` (extend), `go.sum` (extend),
`config/config.go` (extend), `main.go` (extend),
`middleware/middleware.go` (extend), `auth/auth.go` (extend),
`db/models.go` (extend), `db/store.go` (extend),
`db/scrape_configs.go` (extend), `db/events.go` (extend),
`db/migrations/00008_settings.sql` (new), `db/migrations/00009_scrape_context.sql` (new),
`db/settings.go` (new),
`llm/provider.go` (extend), `llm/anthropic.go` (extend), `llm/openai.go` (extend),
`pricing/chain.go` (extend), `pricing/kroger.go` (extend),
`pricing/scraper.go` (extend), `pricing/normalize.go` (extend),
`pricing/cache.go` (extend), `pricing/ai_estimate.go` (extend), `pricing/provider.go` (extend),
`plan/prompt.go` (extend), `plan/repair.go` (extend), `plan/validate.go` (extend),
`plan/job.go` (extend),
`scrape/fetch.go` (extend), `scrape/extractor.go` (extend),
`scrape/recipe.go` (extend),
`barcode/lookup.go` (extend),
`safefetch/fetch.go` (extend),
`web/icons.go` (extend),
`web/render.go` (extend),
`web/routes.go` (extend),
`web/server.go` (extend),
`web/templates/layout.html` (extend),
`web/templates/setup.html` (extend),
`web/templates/plan.html` (extend),
`web/templates/recipe.html` (extend),
`web/templates/recipes.html` (extend),
`web/templates/recipe_import.html` (extend),
`web/templates/meal.html` (extend),
`web/templates/pantry.html` (extend),
`web/templates/search_results.html` (extend),
`web/templates/scan.html` (extend),
`web/templates/shopping_list.html` (extend),
`web/templates/preferences.html` (extend),
`web/templates/login.html` (extend),
`web/templates/index.html` (extend),
`web/static/css/base.css` (extend),
`web/static/css/tokens.css` (extend),
`web/static/css/layout.css` (extend),
`web/static/css/components.css` (extend),
`web/static/css/wizard.css` (extend),
`web/static/js/barcode-camera.js` (extend),
`web/static/js/barcode-hid.js` (extend)

**Flash → Notify migration** (all handlers):
- Replace `s.setFlash(w, msg)` with `s.setNotify(w, NotifySuccess|NotifyDanger|NotifyInfo, msg)`.
- Adds typed notifications (success/danger/info) for toast rendering.

**Encoding fixes**:
- Replace garbled UTF-8 characters (ΓÇö, ΓåÆ, ΓÿÇ∩╕Å, ≡ƒîÖ) with proper UTF-8
  (—, →, ✓, ☼) or SVG icons.

**Config updates**:
- `.env.example`: add new env vars (KROGER_*, LLM_*, RECIPE_IMAGE_DIR, etc.).
- `config/config.go`: new config fields (Kroger, LLM, scrape settings).
- `main.go`: wire new config to server.
- `docker-compose.yml`: update env vars, volume mounts.

**Makefile**: add build/run/test/deploy targets.

**Migration files**:
- `00008_settings.sql`: settings key-value table.
- `00009_scrape_context.sql`: context_json column on scrape_configs.

**DB updates**:
- `db/models.go`: new models (ScrapeConfig with ContextJSON, Settings).
- `db/store.go`: new interface methods.
- `db/settings.go`: new settings CRUD package.
- `db/scrape_configs.go`: context_json support.
- `db/events.go`: minor event logging.

**LLM provider updates**:
- `llm/provider.go`: provider abstraction updates.
- `llm/anthropic.go`: API key handling.
- `llm/openai.go`: compatible endpoint support.

**Pricing updates**:
- `pricing/chain.go`: pricing chain updates.
- `pricing/kroger.go`: Kroger API integration.
- `pricing/scraper.go`: scraper improvements.
- `pricing/normalize.go`: quantity normalization.
- `pricing/cache.go`: cache TTL.
- `pricing/ai_estimate.go`: AI estimate provider.
- `pricing/provider.go`: provider interface.

**Plan updates**:
- `plan/prompt.go`: prompt builder updates.
- `plan/repair.go`: plan repair logic.
- `plan/validate.go`: validation updates.
- `plan/job.go`: job management.

**Scrape updates**:
- `scrape/fetch.go`: fetch improvements.
- `scrape/extractor.go`: selector improvements.
- `scrape/recipe.go`: recipe parser updates.

**Other**:
- `barcode/lookup.go`: barcode lookup updates.
- `safefetch/fetch.go`: SSRF guard updates.
- `web/icons.go`: icon SVG updates.
- `web/render.go`: template helper updates.
- `web/routes.go`: route registration.
- `web/server.go`: server config.
- `middleware/middleware.go`: CSRF origin fix for Docker.
- `auth/auth.go`: auth updates.
- `web/templates/*`: template updates for encoding, notify, icons.
- `web/static/css/*`: CSS updates.
- `web/static/js/barcode-*.js`: barcode JS updates.

```
misc: migrate flash→notify, fix UTF-8 encoding, update configs and migrations
```
Status: ⬜

---

## Summary

7 commits:
1. **Stores catalog + context view** — KnownStores, state-aware picker, scrape context
2. **Pagination** — generic package, wired to history + admin prices
3. **AI logos + settings registry + partials** — provider logos, settings CRUD, reusable templates
4. **Toast + icons + autosave** — universal toast, SVG dark mode icons, wizard CSS
5. **LLM log + plan generation** — debug log page, progress screen, markdown fence fix
6. **Misc cleanup** — flash→notify migration, UTF-8 fixes, config/migration updates

After these commits, all uncommitted work is accounted for and the working tree is clean.
