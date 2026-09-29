# Phase 14 — Desktop app (Tauri) for Windows + Linux (2026-09-28)

Goal: Go Eat runs as a normal desktop app, no server or Docker, on Windows and
Linux. macOS and mobile are out of scope for now.

## Shape

Tauri v2 shell (Rust, OS webview: WebView2 on Windows, WebKitGTK on Linux)
that runs the unchanged Go binary as a **sidecar**:

1. The shell resolves the per-user data dir (`%APPDATA%\com.goeat.desktop`,
   `~/.local/share/com.goeat.desktop`) and keeps a persistent
   `session.key` there (0600).
2. It starts `goeat` with `GOEAT_DESKTOP=1`, `LISTEN_ADDR=127.0.0.1:0`, the
   database and image dirs inside the data dir, the session key, and a fresh
   per-launch `SETUP_TOKEN`.
3. Go binds an ephemeral loopback port and prints one line to stdout:
   `GOEAT_LISTENING http://127.0.0.1:<port>`. The shell reads it and points
   the window there - no port-picking race.
4. First run: the window opens `/setup#token=…`; the wizard reads the token
   from the fragment (never sent to the server or logged) and prefills it.
5. Quitting the app kills the sidecar. If the shell dies instead, Go sees
   stdin close and shuts down, so no orphaned server keeps the port and DB.

Why Tauri over Electron: ~10 MB installers instead of ~150 MB, the OS webview
instead of a bundled Chromium, and the Go app needs no JS/IPC bridge at all -
the window is just a browser pointed at loopback. The page gets **no** Tauri
capabilities (remote origin, empty capability set).

## Desktop-mode hardening (Go side)

- **Host allowlist** (QSS security design §9.3): any website in the user's
  browser can reach `127.0.0.1:<port>`, and DNS rebinding would let it read
  the answers. In desktop mode every request whose `Host` isn't
  `127.0.0.1:<port>` / `localhost:<port>` gets 421. `/health` is exempt.
- CSRF already rejects cross-origin writes (filippo.io/csrf, phase 13).
- Loopback only: desktop mode refuses a non-loopback `LISTEN_ADDR`.

## Price scraping on desktop

FlareSolverr/Browserless aren't there, so pricing falls back to Kroger API →
cache → manual → plain-HTTP scrape → AI estimate. No code change; documented.

## Release + signing (mirrors QSnippet)

Tags: `v*.*.*-release` (from main), `v*.*.*-dev` (prerelease),
`v*.*.*-windows` / `v*.*.*-linux` (one OS, no release). Windows builds an NSIS
installer, Linux a `.deb` and an AppImage. The release job imports
`GPG_PRIVATE_KEY` / `GPG_PASSPHRASE`, writes `SHA256SUMS.txt`, and
detach-signs it and every artifact. No Authenticode: Windows SmartScreen will
warn on first run until/unless a code-signing cert is bought.

## Commits

1. **desktop mode in the Go server** — `GOEAT_DESKTOP`, listen-then-announce,
   loopback-only, Host allowlist, stdin-EOF shutdown, setup token prefill
   from the URL fragment. Tests. Status: done
2. **Tauri shell** — `desktop/` with the sidecar launcher, data dir, session
   key, navigation guard (external links open in the system browser), icons,
   and `make desktop` / build scripts. Status: done
3. **CI + signed releases** — Windows + Linux build workflows and the GPG
   release job. Status: done
