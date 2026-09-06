# Phase 5.7 Commit Plan — Recipe Catalog & Web Import
Date: 2026-09-05
Spec ref: §5.7, §8.2, §10.1, §16.1, §16.4, §16.6

Phase 5.7 adds the recipe catalog (browsable grid of AI + imported recipes),
the web import flow (paste a URL → parse schema.org/JSON-LD/microdata/OpenGraph
→ download image → save), and the SSRF-guarded fetch helper that all
server-side external fetches must use (§16.1). The `scrape/` package gains a
Recipe parser alongside its existing Product parser. Meal photos start
appearing in the plan calendar cells linking to recipe detail.

`RECIPE_IMAGE_DIR` is runtime-writable and served by a dedicated handler —
it lives outside the embedded binary (§16.4).

---

## Commits

### 1. safefetch: SSRF-guarded HTTP fetch helper
Files: `safefetch/fetch.go` (new package)

The one shared fetch helper all external HTTP calls must use (§16.1):
- **Scheme allowlist**: reject anything that is not `http`/`https`.
- **Resolve-then-check**: resolve hostname → reject if any IP is private
  (RFC 1918), loopback, link-local (`169.254.x.x`, `fe80::/10`),
  unique-local, multicast, or unspecified. Uses a custom `DialContext` that
  re-resolves and checks on every new connection so DNS-rebinding is blocked.
- **Redirect cap**: follow at most 3 redirects, re-checking the resolved IP
  on every hop.
- **Size + timeout bounds**: configurable max bytes (default 512 KB) and
  timeout (default 10 s), matching the §11.3 scraper guards.

```go
type Options struct {
    MaxBytes int64
    Timeout  time.Duration
    MaxRedirects int
}
func Fetch(ctx context.Context, rawURL string, opts *Options) (*Result, error)
type Result struct { Body []byte; FinalURL string; StatusCode int }
```

Update `scrape/fetch.go` to delegate its plain `Fetch` to `safefetch.Fetch`
so the scrape-config tool and pricing scraper also benefit.

```
safefetch: add SSRF-guarded HTTP fetch helper
```
Status: ⬜

---

### 2. scrape: add Recipe type and JSON-LD/microdata/OpenGraph parser
Files: `scrape/recipe.go` (new)

```go
type Recipe struct {
    Title       string
    Description string
    ImageURL    string   // original remote URL; downloaded separately
    Servings    int
    PrepMinutes int
    CookMinutes int
    Tags        []string // cuisine, diet labels
    Ingredients []RecipeIngredient
    Steps       []string
    SourceURL   string
    SourceSite  string   // hostname
}
type RecipeIngredient struct { Name, Quantity, Unit string }
```

`ParseRecipe(htmlBody, sourceURL string) (*Recipe, error)`:
1. Try `<script type="application/ld+json">` blocks for `@type:"Recipe"`
   (handles single object, `@graph` array, and nested `@graph`).
2. Fall back to `<meta property="og:…">` / `<meta name="…">` for title and
   image when JSON-LD is absent or incomplete.
3. Microdata (`itemtype="…/Recipe"`) as a third fallback.
4. Return an error only when all three find nothing useful; partial results
   (missing image, missing steps) are acceptable — the editor lets the user
   fill gaps.

```
scrape: add Recipe parser (JSON-LD, microdata, OpenGraph)
```
Status: ⬜

---

### 3. db: add catalog recipe migration
Files: `db/migrations/00007_catalog_recipes.sql`

```sql
CREATE TABLE IF NOT EXISTS catalog_recipes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id  INTEGER NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    title         TEXT    NOT NULL,
    source_kind   TEXT    NOT NULL DEFAULT 'manual', -- 'ai'|'imported'|'manual'
    source_url    TEXT    NOT NULL DEFAULT '',
    source_site   TEXT    NOT NULL DEFAULT '',
    image_path    TEXT    NOT NULL DEFAULT '', -- relative path under RECIPE_IMAGE_DIR
    servings      INTEGER NOT NULL DEFAULT 4,
    prep_minutes  INTEGER NOT NULL DEFAULT 0,
    cook_minutes  INTEGER NOT NULL DEFAULT 0,
    tags          TEXT    NOT NULL DEFAULT '[]', -- JSON array
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS catalog_recipe_ingredients (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_recipe_id INTEGER NOT NULL REFERENCES catalog_recipes(id) ON DELETE CASCADE,
    name             TEXT    NOT NULL,
    quantity         TEXT    NOT NULL DEFAULT '',
    unit             TEXT    NOT NULL DEFAULT '',
    normalized_term  TEXT    NOT NULL DEFAULT '',
    position         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS catalog_recipe_steps (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_recipe_id INTEGER NOT NULL REFERENCES catalog_recipes(id) ON DELETE CASCADE,
    position         INTEGER NOT NULL,
    text             TEXT    NOT NULL
);
```

```
db: add catalog recipe migration
```
Status: ⬜

---

### 4. db: catalog recipe models, store interface, and query implementations
Files: `db/models.go`, `db/store.go`, `db/catalog_recipes.go` (new)

Models:
```go
type CatalogRecipe struct {
    ID, HouseholdID int64
    Title, SourceKind, SourceURL, SourceSite, ImagePath string
    Servings, PrepMinutes, CookMinutes int
    Tags []string
    CreatedAt time.Time
}
type CatalogRecipeIngredient struct {
    ID, CatalogRecipeID int64
    Name, Quantity, Unit, NormalizedTerm string
    Position int
}
type CatalogRecipeStep struct {
    ID, CatalogRecipeID int64
    Position int; Text string
}
type CreateCatalogRecipeParams struct {
    HouseholdID int64
    Title, SourceKind, SourceURL, SourceSite, ImagePath string
    Servings, PrepMinutes, CookMinutes int
    Tags []string
}
```

Store interface additions:
- `CreateCatalogRecipe(ctx, CreateCatalogRecipeParams) (*CatalogRecipe, error)`
- `AddCatalogRecipeIngredient(ctx, CatalogRecipeID int64, name, qty, unit string, pos int) error`
- `AddCatalogRecipeStep(ctx, CatalogRecipeID int64, pos int, text string) error`
- `GetCatalogRecipe(ctx, id int64) (*CatalogRecipe, error)`
- `ListCatalogRecipes(ctx, householdID int64) ([]*CatalogRecipe, error)`
- `ListCatalogRecipeIngredients(ctx, catalogRecipeID int64) ([]*CatalogRecipeIngredient, error)`
- `ListCatalogRecipeSteps(ctx, catalogRecipeID int64) ([]*CatalogRecipeStep, error)`
- `DeleteCatalogRecipe(ctx, id int64) error`

```
db: add catalog recipe models, store interface, and query implementations
```
Status: ⬜

---

### 5. recipes: web import orchestration with image download
Files: `recipes/import.go` (new package)

```go
// Import fetches rawURL, parses a Recipe, downloads the image (if any),
// saves everything to the DB, and returns the new CatalogRecipe ID.
func Import(ctx context.Context, store db.Store, householdID int64,
    rawURL, imageDir string) (int64, error)
```

Steps:
1. `safefetch.Fetch` the URL (no SSRF, size-capped).
2. `scrape.ParseRecipe` the HTML.
3. If `Recipe.ImageURL` is non-empty: `safefetch.Fetch` the image URL, write
   to `imageDir/<uuid>.<ext>`, store the relative path. Image errors are
   non-fatal — import proceeds without a photo.
4. `store.CreateCatalogRecipe` → insert ingredients + steps in order.
5. Return the new ID.

Image licensing note (§16.6): images are stored with `source_url` and
`source_site` for attribution; the feature is personal-household use only.

```
recipes: add web import orchestration with image download
```
Status: ⬜

---

### 6. web: recipe import page
Files: `web/handlers_recipes.go` (new), `web/templates/recipe_import.html` (new)

`GET /recipes/import` — renders an import form: URL input + optional manual
fields (title, servings, tags). Flashes any prior parse error.

`POST /recipes/import` — reads `url` form field, calls `recipes.Import`.
On success: redirects to `/recipes/{id}`. On parse failure: re-renders the
form pre-filled with what was parsed so the user can correct and save.

Manual save path: `POST /recipes/import/manual` — accepts all fields
explicitly (no URL fetch), saves a `source_kind=manual` recipe.

Template: URL field + "Import" button + collapsible manual-edit section
showing the parsed fields after a failed/partial parse.

```
web: add recipe import page
```
Status: ⬜

---

### 7. web: recipe catalog and detail pages
Files: `web/handlers_recipes.go` (extend), `web/templates/recipes.html` (new),
`web/templates/recipe.html` (new), `web/routes.go`, `web/server.go`

`GET /recipes` (`handleRecipesPage`):
- Lists all `catalog_recipes` for the household.
- Accepts `?source=` (ai|imported|manual) and `?tag=` filter params,
  applied server-side.
- Renders a card grid: thumbnail (served from `/recipe-images/<path>` if
  set, else a placeholder icon), title, source badge, servings, prep+cook time.

`GET /recipes/{id}` (`handleRecipeDetail`):
- Loads recipe + ingredients + steps.
- Shows full detail: image, ingredients list, numbered steps, source
  attribution link (if imported), source badge.
- Delete button (POST /recipes/{id}/delete) for owner cleanup.

`GET /recipe-images/{path...}` — serves files from `RECIPE_IMAGE_DIR`
using `http.FileServer` scoped to that directory.

`Server` gains `imageDir string` field (from `cfg.RecipeImageDir`).

`config.Config` gains `RecipeImageDir string` (env `RECIPE_IMAGE_DIR`,
default `./data/recipe-images`).

Routes:
```
GET  /recipes
GET  /recipes/import       (literal — before /recipes/{id})
POST /recipes/import
POST /recipes/import/manual
GET  /recipes/{id}
POST /recipes/{id}/delete
GET  /recipe-images/{path...}
```

```
web: add recipe catalog and detail pages
```
Status: ⬜

---

### 8. spec: mark Phase 5.7 complete
Files: `spec/commit-plan-phase5.7-09-05-26.md` (this file, all ✅)

```
spec: mark all Phase 5.7 commits complete
```
Status: ⬜

---

## Summary

8 commits: SSRF-guarded fetch (updating existing scrape/fetch.go), Recipe
parser in scrape/, catalog DB migration + models + queries, recipes/ import
package with image download, recipe import page, recipe catalog + detail
pages, and spec completion. `RECIPE_IMAGE_DIR` is runtime-writable, served
by a dedicated handler; all external fetches go through `safefetch`.
