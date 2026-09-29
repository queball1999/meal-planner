# Go Eat

AI-assisted weekly meal planner with pantry tracking, recipe catalog, shopping list generation, and store pricing.

## Quick start (Docker)

```sh
# 1. Copy env and set a real SESSION_SECRET
cp .env.example .env
# edit .env — at minimum set SESSION_SECRET to a 32-char random string

# 2. Build and start
docker compose up --build

# 3. Copy the setup token from the log (docker compose logs goeat | grep "Setup token")
# 4. Open http://localhost:8080 and run through the setup wizard
```

The setup wizard asks for a **setup token** so nobody else can claim a fresh install. It is printed once to the server log at startup (`Setup token: …`), or set your own with `SETUP_TOKEN` (24+ hex characters). Anyone who can read that log before setup finishes can claim the instance; the token stops working once setup is done.

One household per server by default. Set `ENABLE_MULTI_TENANT=true` to let an admin create more households and let users switch between the ones they belong to. With it off, everyone works in the household setup created (the oldest one, on a server that already has several); owner / editor / viewer roles apply either way.

Data (SQLite database + recipe images) is stored in the `goeat-data` named Docker volume and survives container restarts. `docker compose down -v` (or `make docker-reset`) deletes that volume and resets the app to the first-run setup wizard; `docker compose down` (or `make docker-down`) leaves it in place. Use `make docker-backup` / `make docker-restore` to archive or restore it - see [Docker details](#docker-details).

## Quick start (local)

Requires Go 1.26+. No C compiler needed — the SQLite driver is pure Go.

```sh
go run . 
```

The server binds to `:8080` by default. Visit `http://localhost:8080` to run the setup wizard, using the `Setup token: …` line printed to the terminal.

## Desktop app (Windows + Linux)

Go Eat also ships as a normal desktop app: no server, no Docker. It's the same Go server, run on `127.0.0.1` by a small [Tauri](https://tauri.app) shell (`desktop/`) that shows it in a native window. The setup token is filled in for you.

| | Installer | Portable |
|---|---|---|
| Windows | `GoEat-<ver>-windows-x64-setup.exe` (per-user, no admin) | `GoEat-<ver>-windows-x64-portable.zip` |
| Linux | `GoEat-<ver>-linux-amd64.deb` | `GoEat-<ver>-linux-amd64.AppImage` |

- **Where data lives:** `%APPDATA%\com.goeat.desktop` on Windows, `~/.local/share/com.goeat.desktop` on Linux. The portable zip keeps everything in a `data` folder next to `Go Eat.exe` instead, because of the `portable.txt` beside it. `goeat.log` in that folder has the server's log.
- **Prices:** the desktop app has no FlareSolverr/Browserless, so store scraping only works for sites that answer plain requests. Pricing falls back to the Kroger API, manual prices, and AI estimates.
- **Windows SmartScreen** warns on first run: the builds are GPG-signed, not Authenticode-signed.

**Verify a download:** every release file has a detached `.sig`, and `SHA256SUMS.txt` is signed too.

```sh
gpg --verify SHA256SUMS.txt.sig SHA256SUMS.txt
sha256sum --check --ignore-missing SHA256SUMS.txt
```

**Build it yourself** (Go, Node and Rust; on Windows also the MSVC C++ build tools, or prefix with `RUSTUP_TOOLCHAIN=stable-x86_64-pc-windows-gnu`). Run from a POSIX shell (Git Bash on Windows); each OS builds its own packages:

```sh
make desktop-windows   # -> output/windows/  installer + portable zip
make desktop-linux     # -> output/linux/    .deb + AppImage (needs libwebkit2gtk-4.1-dev)
make desktop-dev       # run from source
```

Releases are built by `.github/workflows/build_manager.yaml` on a `vX.Y.Z` tag (stable, from `main`) or `vX.Y.Z-dev` tag (prerelease). One tag builds the desktop apps, standalone servers (Linux/Windows/macOS, amd64 + arm64) and the `ghcr.io/queball1999/meal-planner` Docker image, all versioned `X.Y.Z`.

## Configuration

All options are set via environment variables (or `.env`). See [`.env.example`](.env.example) for the full reference. The important ones:

| Variable | Default | Description |
|---|---|---|
| `SESSION_SECRET` | *(required)* | ≥32-char random string. Generate with `openssl rand -hex 32`. |
| `DATABASE_URL` | `file:./data/goeat.db` | SQLite file URI or path. |
| `LISTEN_ADDR` | `:8080` | Host:port to bind. |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Canonical URL (used in redirects). |
| `RECIPE_IMAGE_DIR` | `./data/recipe-images` | Where downloaded recipe images are stored. |
| `WEEK_START_DAY` | `sunday` | `sunday` or `monday`. |
| `PROVIDER` | *(unset)* | `anthropic` \| `openai` \| `openai_compatible`. Enables AI meal generation. |
| `ANTHROPIC_API_KEY` | *(unset)* | Required when `PROVIDER=anthropic`. |
| `KROGER_CLIENT_ID` | *(unset)* | Enables live Kroger pricing (covers Fry's too). See below for credential rotation. |
| `KROGER_CLIENT_SECRET` | *(unset)* | Secret paired with `KROGER_CLIENT_ID`. |
| `KROGER_LOCATION_ID` | *(unset)* | Store location prices are quoted against. |
| `KROGER_MAX_RPM` | `60` | Cap on Kroger API requests per minute (rolling window). |
| `KROGER_DAILY_CAP` | `9500` | Per-credential daily call budget before rotating to the next key (margin under Kroger's 10k/day Products limit). |
| `RENDER_BACKEND` | *(unset)* | `browserless` or `flaresolverr` — headless browser used for scraping. |
| `RENDER_URL` | *(unset)* | Base URL of that service, e.g. `http://browserless:3000` (same Docker network) or `http://localhost:3900` (host, per the port mapping in `docker-compose.yml`). |
| `RENDER_TOKEN` | *(unset)* | Auth token for Browserless - same value as that service's `TOKEN`. |

### Kroger pricing and credential rotation

The Kroger tier authenticates with an OAuth2 client-credentials token (cached in memory,
refreshed ~30s before expiry and again reactively on a rejected token). Every call passes
through a rolling one-minute rate limiter, and transient `429`/`5xx` responses are retried
with backoff that honours a `Retry-After` header.

To survive one key being throttled or spending its daily quota, register more than one
Kroger app and pass **comma-separated, position-aligned** lists:

```
KROGER_CLIENT_ID=app1id,app2id
KROGER_CLIENT_SECRET=app1secret,app2secret
```

Each credential keeps its own token and its own `KROGER_DAILY_CAP` counter. On a `429`,
auth failure, or daily-cap exhaustion the client parks that key (for the server's
`Retry-After`, else 60s) and rotates to the next. When every key is unavailable the tier
simply yields to the next pricing tier — it never blocks the request. A single value on
each line (no comma) is the normal one-credential setup.

### Scraping stores that fight back

Prices are resolved in tiers: live API → cache → manual → scrape → AI estimate. The
scrape tier fetches a store's search page with browser headers and a cookie jar. That is
enough for some chains and not others - the rest either block non-browser clients outright
or render their prices in JavaScript, leaving the HTML empty.

For those, `docker-compose.yml` already includes two optional headless-browser
services alongside the app - comment out either block (or both) if you don't need them:

| Service | Port | Use it when |
|---|---|---|
| `browserless` | 3000 | The page loads but has no prices in its markup (client-rendered). |
| `flaresolverr` | 8191 | The store answers 403 or "Just a moment..." (Cloudflare-style challenge). |

Then set `RENDER_BACKEND` / `RENDER_URL` (in `.env`, or on the Settings page where the
change applies immediately) and click **Test headless browser** on the Settings page.
Scraping falls back to it automatically whenever a direct fetch comes back as a challenge
page or an empty shell.

Per store, **Settings → Scraping** offers three modes: *Assisted* (click the price in a
sandboxed preview to pick its selector), *Auto* (structured data plus built-in patterns for
common retail markup), and *Auto + AI* (the page is condensed and the model names the
selectors - every proposal is re-run against the live page before it is offered, and a store
in this mode falls back to letting the model read the page when its selectors stop working).

## Architecture

```
main.go              — entrypoint, wires config → db → web server
config/              — env loading (config.Load)
db/                  — SQLite store; all SQL lives here; db.Store interface
web/                 — HTTP handlers, templates (html/template), embedded static assets
  handlers_*.go      — one file per feature area
  templates/         — {{define "content"}} blocks rendered into layout.html
  static/            — CSS (tokens → base → layout → components) + JS
recipes/             — import.go: URL import and manual save orchestration
scrape/              — CSS/XPath price scraper; JSON-LD / OpenGraph / Microdata recipe parser
search/              — unified search across recipes, pantry, and meal history
barcode/             — UPC/EAN lookup: pantry items → product map → nil
llm/                 — provider-agnostic LLM client (Anthropic, OpenAI, Ollama)
pricing/             — quantity normalisation, Kroger OAuth2 client, scraper provider
middleware/          — auth, CSRF, rate-limit wrappers
safefetch/           — SSRF-safe HTTP client used for web imports and scraping
```

## Development

```sh
# Run with live .env
go run .

# Tidy modules
go mod tidy

# Build a local binary
go build -o goeat .
```

Templates and static assets are embedded at build time via `//go:embed`; no separate asset server is needed.

## Docker details

The Dockerfile uses a two-stage build:

1. **builder** — `golang:1.26-bookworm`, `CGO_ENABLED=0`, produces a static binary.
2. **runtime** — `gcr.io/distroless/static-debian12`, no shell, minimal attack surface.

The `/data` directory is declared as a `VOLUME`; `docker-compose.yml` mounts it to the named volume `goeat-data`, so it persists across restarts and rebuilds but not across `docker compose down -v`.

Common `make` targets:

| Target | Effect |
|---|---|
| `docker-up` | Start the stack (no rebuild) |
| `docker-rebuild` | Rebuild the image and restart the container |
| `docker-down` | Stop the stack, keep `goeat-data` |
| `docker-reset` | Stop the stack and delete `goeat-data` (resets db + setup wizard) |
| `docker-backup` | Archive `goeat-data` + `.env` to `backups/goeat-backup-<timestamp>.zip` |
| `docker-restore BACKUP_FILE=...` | Restore a `docker-backup` archive into `goeat-data` |
