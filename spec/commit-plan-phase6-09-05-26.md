# Phase 6 Commit Plan — Polish
Date: 2026-09-05
Spec ref: §8.5, §11.4, §16.9

Phase 6 hardens the UI and wraps up the project for deployment:
- Full mobile-responsive CSS (hamburger nav, card-stacked tables, touch targets).
- Reduced-motion-aware animations (§8.5).
- Dark mode completion (explicit data-theme toggle + system preference).
- Settings page (AI + pricing provider config, week start day, animated toggles).
- Accessibility pass (focus management, skip link, ARIA labels, colour contrast).
- README covering setup, configuration, and self-hosted deployment.
- Dockerfile (multi-stage, distroless final image) + docker-compose.yml.

---

## Commits

### 1. css: mobile-responsive layout and touch-friendly components ✅
Files: `web/static/css/layout.css` (extend), `web/static/css/base.css` (extend),
`web/static/css/components.css` (extend), `web/templates/layout.html` (hamburger nav)

Breakpoint: `@media (max-width: 768px)`

- **Nav**: hamburger button (`#nav-toggle`) collapses `.site-nav` into a full-width
  dropdown; `main.js` toggles `aria-expanded` + `nav-open` class on `<body>`.
- **Header**: single row with logo + hamburger; search and user controls collapse
  behind the nav toggle on narrow screens.
- **Data tables**: `.data-table` gains `display:block` with `[data-label]` pseudo
  elements (`content: attr(data-label)`) for card-stacked layout below 600 px.
  Each `<td>` shows its column name above the value.
- **Touch targets**: minimum 44×44 px for all interactive elements on mobile
  (`.btn`, `.nav-link`, checkbox, toggle).
- **Plan calendar**: wraps from 7-column grid to a single-column day stack.
- **Form rows**: `.field-row` stacks vertically on mobile.
- Viewport meta already present; no changes to HTML head required.

```
css: add mobile-responsive layout, hamburger nav, and card-stacked tables
```
Status: ✅

---

### 2. css: animations and reduced-motion support
Files: `web/static/css/base.css` (extend), `web/static/css/components.css` (extend)

- **Page transitions**: subtle fade-in on `.site-main` (`opacity 0→1`, 120 ms).
- **Flash banner**: slide-down + fade-in on `.flash`.
- **Button press**: `transform: scale(0.97)` on `:active`.
- **Skeleton shimmer**: `@keyframes shimmer` on `.skeleton-card` placeholder.
- **Toggle animation**: the `<input type="checkbox">` animated toggle used in
  preferences and settings — smooth thumb slide.
- All animations wrapped in `@media (prefers-reduced-motion: no-preference)` so
  they are stripped for users who prefer reduced motion (§8.5).

```
css: add reduced-motion-aware animations and skeleton shimmer
```
Status: ✅

---

### 3. web: settings page ✅
Files: `web/handlers_settings.go` (new), `web/templates/settings.html` (new),
`web/routes.go`

`GET /settings` — renders the settings page with three sections:

**AI provider** (read-only display of active config; operator edits `.env`):
- Active provider, model name.
- LLM connectivity status (ping via a test generation call — or just display
  `HasLLM` bool from the existing server field).

**Pricing**:
- Week start day: animated toggle "Sunday / Monday" → `POST /settings/week-start`.
- Price cache TTL display (read-only; set via `PRICE_CACHE_TTL_HOURS`).

**Household**:
- Link to `/preferences` for diet tags, cuisines, slot hints.
- "Change password" link (deferred to post-v1 if not wired yet).

`POST /settings/week-start` — saves `WEEK_START_DAY` preference. Because Go Eat
reads config at startup, this writes a hint to the DB (household table) or a
simple `settings` key-value table rather than the env. For v1, use household
`week_start_day` column if one is added, otherwise display only and instruct
operator to set the env var. Implementation: display-only for v1; the toggle
shows the current value but links to the env-var documentation.

Routes: `GET /settings`

```
web: add settings page with AI status, pricing config, and week-start display
```
Status: ✅

---

### 4. web: accessibility pass ✅
Files: `web/templates/layout.html`, several templates

- **Skip link** (`<a href="#main-content" class="skip-link">`) — already present;
  verify it's visible on focus in all themes.
- **Focus ring**: `outline: 2px solid var(--color-focus); outline-offset: 2px`
  on `:focus-visible` for all interactive elements.
- **ARIA labels**: audit all icon-only buttons and ensure each has `aria-label`.
- **Landmark roles**: `<header role="banner">`, `<main>`, `<footer role="contentinfo">`
  — verify present everywhere.
- **Colour contrast**: verify foreground/background combinations meet WCAG AA
  (4.5:1 for normal text, 3:1 for large text) in both light and dark themes.
- **Form labels**: every `<input>` has an associated `<label>` or `aria-label`.
- **Live regions**: flash banners use `role="alert"`.

```
web: accessibility pass — focus rings, ARIA labels, contrast, live regions
```
Status: ✅

---

### 5. deploy: README, Dockerfile, and docker-compose ✅
Files: `README.md` (new), `Dockerfile` (new), `docker-compose.yml` (new),
`.dockerignore` (new)

**README.md** sections:
1. What it is (one paragraph).
2. Quick start (Docker Compose, two commands).
3. Configuration reference (all env vars in a table: name, default, description).
4. Self-hosted setup (reverse proxy note, HTTPS for camera scanning, backup).
5. Development (Go 1.26+, `go run ./cmd/goeat`, `.env` template).
6. Architecture overview (package map, one paragraph).
7. License.

**Dockerfile** — multi-stage:
- Stage 1 (`builder`): `golang:1.26-bookworm`; `go build -o /goeat ./cmd/goeat`.
- Stage 2 (`final`): `gcr.io/distroless/static-debian12:nonroot`; copies the
  binary only. No shell, no package manager, minimal attack surface.
- `EXPOSE 8080`; `ENTRYPOINT ["/goeat"]`.
- Binary is statically linked via `CGO_ENABLED=0 GOOS=linux`.
- SQLite driver (`modernc.org/sqlite`) is CGo-free so this works without CGo.

**docker-compose.yml**:
```yaml
services:
  goeat:
    build: .
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
    environment:
      DATABASE_URL: file:/data/goeat.db
      RECIPE_IMAGE_DIR: /data/recipe-images
      SESSION_SECRET: ""   # REQUIRED — set before running
      # Optional: ANTHROPIC_API_KEY, LLM_API_URL, LLM_MODEL, LLM_API_KEY, etc.
    env_file:
      - .env
```

**.dockerignore**: `spec/`, `*.md` (except README), `.git`, `*.test`, `testdata/`.

```
deploy: add README, Dockerfile, and docker-compose
```
Status: ✅

---

### 6. spec: mark Phase 6 complete ✅
Files: `spec/commit-plan-phase6-09-05-26.md` (this file, all ✅)

```
spec: mark all Phase 6 commits complete
```
Status: ✅

---

## Summary

6 commits: mobile-responsive CSS, reduced-motion animations, settings page,
accessibility pass, README + Dockerfile + docker-compose, spec completion.
After this commit, Go Eat v1 is feature-complete and ready for self-hosted
deployment.
