# Go Eat

AI-assisted weekly meal planner with pantry tracking, recipe catalog, shopping list generation, and store pricing.

## Quick start (Docker)

```sh
# 1. Copy env and set a real SESSION_SECRET
cp .env.example .env
# edit .env — at minimum set SESSION_SECRET to a 32-char random string

# 2. Build and start
docker compose up --build

# 3. Open http://localhost:8080 and run through the setup wizard
```

Data (SQLite database + recipe images) is stored in `./data/` on the host and survives container restarts.

## Quick start (local)

Requires Go 1.26+. No C compiler needed — the SQLite driver is pure Go.

```sh
go run . 
```

The server binds to `:8080` by default. Visit `http://localhost:8080` to run the setup wizard.

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
| `KROGER_CLIENT_ID` | *(unset)* | Enables live Kroger pricing. |

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

The `/data` directory is declared as a `VOLUME`; mount it to persist state across restarts.
