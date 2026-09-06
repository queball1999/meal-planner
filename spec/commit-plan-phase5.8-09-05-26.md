# Phase 5.8 Commit Plan — Search, Filters & Barcode
Date: 2026-09-05
Spec ref: §8.4c, §8.4d, §11.3a, §16.8

Phase 5.8 adds:
- Unified `/search` across recipe catalog, pantry, and meal history.
- Server-side URL-reflected table filters for the recipe catalog and pantry pages.
- A `barcode/` package with `Lookup()` + `GET /scan/{code}` redirect handler.
- Barcode intake for the pantry via HID keystrokes (`barcode-hid.js`) and the
  native `BarcodeDetector` API (camera scanning).

Scope limits (§16.8): scan/consume only — no label generation, no QR printing.
QuaggaJS is MIT-licensed and vendored under `/static/js/quagga.min.js`; not CDN.

---

## Commits

### 1. search: unified search package
Files: `search/search.go` (new package)

```go
type Result struct {
    Kind  string // "recipe" | "pantry" | "meal"
    ID    int64
    Title string
    Extra string // source site, slot+date, unit
    URL   string // destination href
}

type Options struct {
    Partial bool // LIKE '%q%' instead of exact prefix
    Fuzzy   bool // case-fold + strip punctuation before matching
    Limit   int  // per-kind cap, default 10
}

func Search(ctx context.Context, store db.Store, householdID int64,
    q string, opts Options) ([]Result, error)
```

Strategy per kind:
- **recipes**: `LIKE '%q%'` on `title` + `tags` JSON text.
- **pantry**: `LIKE '%q%'` on `name` + `normalized_term`.
- **meals**: `LIKE '%q%'` on `title` across all plan meals for the household
  (joins through `plans`).

Fuzzy pre-processing: lowercase + strip `[^a-z0-9 ]`. Applied to both the
query and the DB values only when `opts.Fuzzy = true` (server-side,
SQLite `lower()` + regex not available so normalize query before issuing LIKE).

Exact check: before running LIKE queries, run an equality check on `title`;
if exactly one match, return it flagged as exact so the handler can redirect.

Store interface additions (`db/store.go`):
```go
SearchCatalogRecipes(ctx context.Context, householdID int64, q string) ([]*CatalogRecipe, error)
SearchPantryItems(ctx context.Context, householdID int64, q string) ([]*PantryItem, error)
SearchMealTitles(ctx context.Context, householdID int64, q string) ([]*Meal, error)
```

Implementations in `db/search.go` (new) using parameterized LIKE.

```
search: add unified search package with exact/partial/fuzzy matching
```
Status: ⬜

---

### 2. web: /search handler and results template
Files: `web/handlers_search.go` (new), `web/templates/search_results.html` (new),
`web/routes.go`

`GET /search?q=&partial=1&fuzzy=1` (`handleSearch`):
- Parse `q`. If blank, redirect to `/`.
- Call `search.Search` with opts from query params (defaults from household
  preferences if we later add them; for now default `Partial=true, Fuzzy=false`).
- If exactly one result and it's flagged exact: `302` to `result.URL`.
- Otherwise render `search_results.html`.

Template:
- Search bar pre-filled with `q`.
- Three sections: Recipes, Pantry, Meals — each a small card list with title +
  extra, linking to the item. Empty sections are hidden.
- "No results" state if all three are empty.

Route: `GET /search` (literal, before any wildcard).

Also add the header search bar to `layout.html`: a collapsible `<input>` with
`id="header-search"` that submits `GET /search?q=` on Enter or icon click.
`main.js` handles collapse/expand via the existing event pattern.

```
web: add /search handler, results template, and header search bar
```
Status: ⬜

---

### 3. web: server-side table filters for recipe catalog and pantry
Files: `web/handlers_recipes.go` (extend), `web/handlers_pantry.go` (extend),
`web/templates/recipes.html` (extend), `web/templates/pantry.html` (extend)

**Recipe catalog filters** (`GET /recipes?q=&tag=&source=`):
- `q`: title text filter (LIKE or search package).
- `tag`: exact tag match (JSON array contains).
- `source`: `ai` | `imported` | `manual`.
- Filters are applied server-side by extending `ListCatalogRecipes` or adding
  `FilterCatalogRecipes(ctx, householdID int64, f CatalogRecipeFilter)` to the
  store. The URL reflects the active filters (shareable, back-button-safe).
- Template: filter bar above the table — text input + tag dropdown + source
  dropdown + "clear filters" link. Empty result renders "No recipes match these
  filters."

Store addition:
```go
FilterCatalogRecipes(ctx context.Context, householdID int64,
    f CatalogRecipeFilter) ([]*CatalogRecipe, error)

type CatalogRecipeFilter struct {
    Q      string
    Tag    string
    Source string
}
```

**Pantry filter** (`GET /pantry?q=`):
- Text filter on `name`. Same URL-reflect pattern, "clear filters" link.
- Extend `ListPantryItems` or add `FilterPantryItems(ctx, householdID int64, q string)`.

Store addition:
```go
FilterPantryItems(ctx context.Context, householdID int64, q string) ([]*PantryItem, error)
```

```
web: add server-side filters for recipe catalog and pantry pages
```
Status: ⬜

---

### 4. barcode: Lookup package and /scan/{code} handler
Files: `barcode/lookup.go` (new package), `web/handlers_scan.go` (new),
`web/routes.go`

```go
// barcode/lookup.go
type Match struct {
    Kind string // "pantry" | "product" | "recipe"
    ID   int64
    URL  string // destination to navigate to
}

func Lookup(ctx context.Context, store db.Store, householdID int64,
    code string) (*Match, error)
```

Resolution order (§8.4d):
1. `pantry_items.barcode = code` → `Match{Kind:"pantry", URL:"/pantry"}`
   (pantry doesn't have per-item detail pages; redirect to list with flash).
2. `item_product_map.barcode = code` → `Match{Kind:"product", URL:"/admin/prices"}`
   (product mapping exists; redirect to price admin with that product highlighted).
3. No match → `nil, nil` (caller renders "no match" page).

`GET /scan/{code}` (`handleScan`):
- URL-decode `code`, call `barcode.Lookup`.
- If match: `302` to `match.URL`.
- If nil: render a small "Unknown barcode" page with the scanned code and a
  link to add it as a pantry item.

Also `GET /scan` (no code) → renders the camera-scan UI (a modal-style page
embedding the camera component, so it can be linked from the pantry page).

Routes:
```
GET /scan
GET /scan/{code}
```

Store additions:
```go
GetPantryItemByBarcode(ctx context.Context, householdID int64, code string) (*PantryItem, error)
GetItemProductMapByBarcode(ctx context.Context, storeID int64, code string) (*ItemProductMap, error)
```

Implementations in `db/pantry.go` (extend) and `db/prices.go` (extend).

```
barcode: add Lookup package and /scan/{code} resolve-and-redirect handler
```
Status: ⬜

---

### 5. web: barcode intake for pantry (HID + camera)
Files: `web/static/js/barcode-hid.js` (new),
`web/static/js/barcode-camera.js` (new),
`web/static/js/quagga.min.js` (vendored),
`web/handlers_pantry.go` (extend — add `POST /pantry/scan`),
`web/templates/pantry.html` (extend — scan button + modal),
`web/routes.go`

**`barcode-hid.js`** — keystroke-gap heuristic (ported from QInventory2.0):
- Listens for `keydown` on the document.
- Characters arriving < 60 ms apart are buffered; if terminated by `Enter` and
  buffer ≥ 6 chars, treats it as a barcode scan.
- Targets the focused `.barcode-field` if present; otherwise posts to
  `data-scan-endpoint` on the active form. Fires a custom `barcode:scanned`
  event with `{code}` detail.

**`barcode-camera.js`** — camera-based scanning:
- Tries `BarcodeDetector` (Chrome/Safari native). Falls back to QuaggaJS.
- Opens a `<dialog>` with a `<video>` feed; corner brackets animate through
  idle → detecting → confirmed colour states (CSS custom properties).
- Two-read confirm threshold before accepting a code (avoids misreads).
- Fires `barcode:scanned` on the dialog's opener element.
- Torch toggle via `ImageCapture.setPhotoCapabilities`.

**`POST /pantry/scan`** (`handlePantryScan`):
- Reads `barcode` form field.
- Calls `barcode.Lookup`; if pantry hit, increments quantity by 1; if no hit,
  creates a new pantry item with `Barcode=code, Name=code` (user edits the name
  after — same UX as QInventory). Returns redirect to `/pantry`.

Pantry page changes:
- "Scan" button in the add-item row; opens camera modal or fires HID listener.
- `barcode` column in the pantry table (hidden when all empty).
- A `data-barcode-field` attribute on the barcode input that `barcode-hid.js`
  watches.

Store addition:
```go
IncrementPantryItem(ctx context.Context, id int64, delta float64) error
GetPantryItemByBarcode(ctx context.Context, householdID int64, code string) (*PantryItem, error)
```

Route: `POST /pantry/scan`

```
web: add barcode intake to pantry page (HID keystroke + camera scanning)
```
Status: ⬜

---

### 6. spec: mark Phase 5.8 complete
Files: `spec/commit-plan-phase5.8-09-05-26.md` (this file, all ✅)

```
spec: mark all Phase 5.8 commits complete
```
Status: ⬜

---

## Summary

6 commits: unified search package + handler, recipe/pantry table filters, barcode
Lookup + scan handler, barcode HID/camera JS + pantry scan endpoint, spec
completion. All external fetches (camera init aside) stay within the existing
safefetch/auth boundary. QuaggaJS is vendored, not CDN.
