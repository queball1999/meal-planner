# Commit Plan — meal-planner (Phase 0 + Phase 1)

**10 commits, all on the initial branch.**  
`make check` passes after commit 10 (the final one). Because several files in
the `web` package were written once in their Phase 1 form (the Phase 0 handler
stubs and Phase 1 CSRF/session wiring live in the same `web/server.go`,
`web/routes.go`, `web/render.go`, and `web/handlers.go`), those files are
committed whole in the commit where they logically belong rather than
hunk-split. Commits 1–5 and 10 are independently buildable/testable; commits
6–9 together form the complete `web` package, which compiles cleanly after
commit 9.

Exclude from every `git add`:

- `bin/` (build artefact)
- `data/` (SQLite runtime files — WAL, SHM)

---

## 1. ✅ chore: scaffold Go module, Makefile, and repo configuration

Module declaration, dependency lock, ignore rules, line-ending policy, build
targets, and the dotenv example. Everything that must exist before the first
line of Go.

**Stage:**
```
git add go.mod go.sum .gitignore .gitattributes Makefile .env.example spec/spec.md
```

**Commit message:**
```
chore: scaffold Go module, Makefile, and repo configuration

- Module goeat, Go 1.26; direct dependencies declared (godotenv, goose/v3, modernc.org/sqlite, gorilla/csrf, golang.org/x/crypto).
- Makefile targets: check (fmt + vet + test), build, run, deps, clean.
- .gitattributes enforces LF line endings on all text files.
- .env.example documents every config var with phase annotations.
- spec/spec.md committed as the living reference for all subsequent work.
```

---

## 2. ✅ feat(config): load dotenv configuration with ephemeral session secret warning

`config.Load` reads a `.env` file then falls back to the environment, with a
hard warning (not a fatal error) when `SESSION_SECRET` is missing so the
server still starts on a first run but the operator knows the secret is
ephemeral. `SessionTTLHours` defaults to 168 (one week).

**Stage:**
```
git add config/config.go config/config_test.go
```

**Commit message:**
```
feat(config): load dotenv configuration with ephemeral session secret warning

- SESSION_SECRET missing → loud stderr warning and a random fallback; server starts but sessions don't survive restarts.
- SessionTTLHours defaults to 168 (one week); all other fields have sensible zero-value defaults.
- Tests cover defaults and per-var env overrides.
```

---

## 3. ✅ feat(db): open SQLite with WAL/FK pragmas, embed goose migrations, and implement full query layer

All database work lives here: the file-open lifecycle (`db.go`), the append-
only SQL migrations embedded via `go:embed` (`migrations/`), the `Store`
interface with every method the application needs (`store.go`), the `User`,
`Session`, `Household`, and `AppEvent` model structs (`models.go`), and the
four query-implementation files (`users.go`, `sessions.go`, `households.go`,
`events.go`).

Phase 0 migration (`00001_skeleton.sql`) creates `settings` and `app_events`.
Phase 1 migration (`00002_auth.sql`) adds `users`, `sessions`, and
`households`. Both migrations are included here so the `Store` interface and
its implementors compile together — splitting the migrations across commits
would leave the interface unsatisfied.

`GetHousehold` returns `nil, nil` on no-rows; callers use `if hh == nil` to
drive the first-run redirect. `CreateSession` stores only the SHA-256 hash of
the raw token; the raw token never touches the database.

**Stage:**
```
git add db/db.go db/db_test.go
git add db/migrations/00001_skeleton.sql db/migrations/00002_auth.sql
git add db/store.go db/models.go
git add db/users.go db/sessions.go db/households.go db/events.go
```

**Commit message:**
```
feat(db): open SQLite with WAL/FK pragmas, embed goose migrations, and implement full query layer

- WAL journal mode + foreign-key enforcement set via PRAGMA at open time.
- Migrations embedded with go:embed and applied by goose; append-only, never edited after first apply.
- Store interface covers user CRUD, session lifecycle, household creation/fetch, and audit-event logging.
- CreateSession stores token_hash (SHA-256), never the raw token.
- GetHousehold returns nil, nil on no rows so callers drive first-run setup without a sentinel error.
- LogEvent is best-effort (errors logged, never surfaced to callers).
```

---

## 4. ✅ feat(auth): add bcrypt password hashing and secure session token generation

Self-contained package. No internal imports. `HashPassword`/`CheckPassword`
use bcrypt cost 12. `ValidatePassword` enforces the minimum-length rule in
one place so handlers don't duplicate it. `GenerateToken` returns 32 random
bytes as a hex string; `HashToken` SHA-256s it for storage.

**Stage:**
```
git add auth/auth.go auth/auth_test.go
```

**Commit message:**
```
feat(auth): add bcrypt password hashing and secure session token generation

- bcrypt cost 12; ValidatePassword enforces the minimum-length rule in one place.
- GenerateToken: 32 random bytes (crypto/rand), returned as hex; HashToken: SHA-256 hex for DB storage.
- Tests cover round-trip hash/check, wrong-password rejection, length validation, and hash determinism.
```

---

## 5. ✅ feat(middleware): add context-injected session loading and RequireAuth guard

`LoadSession` reads the `goeat_session` cookie, hashes the raw token, looks it
up in the store, and injects the `*db.User` and `*db.Household` into the
request context. Expired sessions are deleted and treated as absent. Sessions
belonging to a household that has not been created yet are valid — the
dashboard handler deals with that case. `RequireAuth` redirects to `/auth/login`
(with the current path as `?next=`) when no user is in context.
`ClientIP` walks `X-Forwarded-For` → `X-Real-IP` → `RemoteAddr` for audit
logging.

**Stage:**
```
git add middleware/middleware.go
```

**Commit message:**
```
feat(middleware): add context-injected session loading and RequireAuth guard

- LoadSession: cookie → SHA-256 → store lookup; expired sessions deleted and treated as absent.
- UserFromCtx / HouseholdFromCtx are the only way handlers read session state; no handler peeks at the cookie directly.
- RequireAuth redirects to /auth/login?next=<path> rather than returning 401, since every protected surface is a browser page.
- ClientIP: X-Forwarded-For → X-Real-IP → RemoteAddr for accurate audit records behind a reverse proxy.
```

---

## 6. feat(web): add CSS design token system, component library, and dark-mode toggle script

Pure static assets — no Go compilation involved. `tokens.css` defines the
full two-tier custom-property palette (warm-green accent `#43a047`, neutral
scale, semantic semantic colours, layout, spacing, typography, shadows, and
motion tokens) with a `[data-theme="dark"]` / `prefers-color-scheme` swap.
`base.css` adds the reset, focus rings, `.sr-only`, and reduced-motion
respect. `layout.css` covers the skip link, sticky header, nav, main, footer,
and the mobile breakpoint. `components.css` covers buttons (all variants),
cards, badges, animated toggle switch, skeleton placeholder, form fields,
flash/toast messages, and the Phase 1 auth/setup surfaces. `main.js`
implements the dark-mode toggle with `localStorage` persistence.

**Stage:**
```
git add web/static/css/tokens.css web/static/css/base.css
git add web/static/css/layout.css web/static/css/components.css
git add web/static/js/main.js
```

**Commit message:**
```
feat(web): add CSS design token system, component library, and dark-mode toggle script

- Two-tier token architecture: primitive values on :root, semantic aliases consumed everywhere else.
- Dark mode: [data-theme="dark"] wins over prefers-color-scheme; JS toggle persists to localStorage.
- Components.css covers all button variants, cards, badges, the animated toggle switch, flash messages, and auth/setup page surfaces.
- No hardcoded colour literals in layout or component rules — everything traces back to a token.
```

---

## 7. ✅ feat(web): add MDI inline-SVG icon helper and html/template rendering pipeline

`embed.go` declares the `go:embed` directives for `static/` and `templates/`.
`icons.go` exposes an `iconFunc` template function that returns inline `<svg>`
markup from a path map covering every icon the UI currently uses (home,
calendar, cart, archive, fork, cog, refresh, check, alert, help, search,
logout, pencil, delete). `render.go` defines `pageData` — the struct threaded
through every page render — including `User`, `Household`, `CSRFField`, and
`Flash`. `renderPage` parses `layout.html` plus the per-page template on every
request (no global parse cache yet) so template edits take effect on reload.
`layout.html` is the sticky-header shell with a conditional nav (only when
`.User != nil`), a sign-out form in the user menu, flash display, and the
dark-mode toggle button. `index.html` is the Phase 0 dashboard placeholder
with phase-progress badges.

**Stage:**
```
git add web/embed.go web/icons.go web/render.go
git add web/templates/layout.html web/templates/index.html
```

**Commit message:**
```
feat(web): add MDI inline-SVG icon helper and html/template rendering pipeline

- iconFunc injects inline SVG paths at template execution time; no separate sprite sheet or img tag.
- pageData.CSRFField is a template.HTML carrying the hidden CSRF input; handlers never write raw HTML.
- pageData.Flash is populated by popFlash (cookie-based) so flash messages survive a redirect.
- layout.html's nav and user menu are fully conditional on pageData.User; unauthenticated pages get a clean header.
```

---

## 8. ✅ feat(web): add HTTP server, CSRF middleware, session wiring, route table, health endpoint, and cookie helpers

`server.go` builds the handler chain: CSRF (`gorilla/csrf`, `Secure(false)` +
`SameSiteLaxMode` for LAN HTTP) wraps `LoadSession` wraps the mux. The
`Server` struct holds the assembled `http.Handler`, not a bare mux, so
middleware is applied once at construction and can never be bypassed by adding
a route. `routes.go` is the single route table; `RequireAuth` is applied per-
route rather than globally. `handlers.go` has the health-check JSON endpoint
and the dashboard handler (redirects to `/setup` when `GetHousehold` returns
nil). `helpers.go` contains `setSessionCookie` / `clearSessionCookie`
(HttpOnly, SameSiteLax, 30-day max age), `setFlash` / `popFlash` (cookie-
based, 30-second max age for redirect round-trips), and `logEvent` (thin
wrapper around `db.LogEvent` that absorbs the error and logs it).

**Stage:**
```
git add web/server.go web/routes.go web/handlers.go web/helpers.go
```

**Commit message:**
```
feat(web): add HTTP server, CSRF middleware, session wiring, route table, health endpoint, and cookie helpers

- Handler chain: gorilla/csrf → LoadSession → mux; Secure(false) + SameSiteLaxMode required for LAN HTTP (SameSite alone is not enough).
- Server holds an assembled http.Handler so middleware cannot be bypassed by a stray route addition.
- Dashboard handler redirects to /setup when GetHousehold returns nil; no explicit "setup done" flag needed.
- setFlash/popFlash are cookie-based with 30-second MaxAge so flash messages survive a single redirect and self-expire if the next page never reads them.
- /health returns JSON {"ok":true,"version":"..."} and bypasses auth; used by load-balancer health checks.
```

---

## 9. ✅ feat(web): add login, logout, and first-run setup wizard

`handlers_auth.go` handles `GET /auth/login` (render form), `POST /auth/login`
(validate → create session → set cookie → redirect), and `POST /auth/logout`
(delete session → clear cookie → redirect). The login `POST` path performs a
dummy bcrypt comparison on an unknown username so response time does not leak
whether the username exists. `handlers_setup.go` handles `GET /setup` and
`POST /setup`; the `POST` validates all fields (username ≥ 3 chars, passwords
match, password ≥ 8 chars, household size 1–20, budget > 0, valid ZIP, known
timezone), creates the user + household + session in one logical block, sets
the session cookie, and redirects to `/`. `login.html` and `setup.html` use
the `{{.CSRFField}}` injection and the `.field` / `.auth-wrap` / `.setup-card`
CSS classes defined in Phase 0's component styles.

**Stage:**
```
git add web/handlers_auth.go web/handlers_setup.go
git add web/templates/login.html web/templates/setup.html
```

**Commit message:**
```
feat(web): add login, logout, and first-run setup wizard

- Login POST performs a dummy bcrypt compare on an unknown username to equalise response time regardless of whether the username exists.
- Logout deletes only the current session (not all user sessions) and clears the cookie with MaxAge=-1.
- Setup POST validates all fields before touching the database; a single validation error re-renders the form without partial writes.
- The setup handler is reachable when no household exists (dashboard redirects there); once a household row is present it redirects away to /.
- Session cookie: HttpOnly, SameSiteLax, 30-day max age; raw token in cookie, SHA-256 hash stored in DB.
```

---

## 10. ✅ feat: wire config, db, and web server into main with signal-context graceful shutdown

Entry point only. Loads config, opens and migrates the database, constructs
the web server, and calls `Run` with a context that cancels on `SIGINT` or
`SIGTERM` so in-flight requests drain before the process exits.

**Stage:**
```
git add main.go
```

**Commit message:**
```
feat: wire config, db, and web server into main with signal-context graceful shutdown

- Config → db.Open → db.Migrate → web.NewServer → Run; each step exits on error with a human-readable message.
- signal.NotifyContext(SIGINT, SIGTERM) gives the server up to the OS default to drain open connections.
- DB Close deferred after Run returns so WAL checkpoint has a chance to complete.
```

---

## Suggested apply order

```
1 (scaffold) → 2 (config) → 3 (db) → 4 (auth) → 5 (middleware) →
6 (CSS/JS) → 7 (templates + render) → 8 (server + routes + handlers) →
9 (auth handlers + setup wizard) → 10 (main)
```

1 and 2 are fully independent of each other and of 3–10. 3 must precede 4
and 5 (auth and middleware import db types). 4 and 5 must precede 6–9 (the
web package imports both). 6 and 7 can be swapped — they share no compile
dependency. 8 and 9 must follow 7 (they reference types from render.go and
icons.go). 10 must be last.

---

**Status: All 10 commits complete. ✅**

Run `make check` after commit 10 to verify the full tree. To validate
individual packages as you go, run `go test ./config/...` after commit 2,
`go test ./db/...` after commit 3, and `go test ./auth/...` after commit 4.
