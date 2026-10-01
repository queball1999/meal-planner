# Phase 15 — Update check, and installing updates on desktop (2026-09-30)

Goal: every Go Eat install knows when a newer stable release is out. The
desktop app can install it from inside the app, using QUpdateTool
(`queball1999/QUpdateTool2.0`). Servers (Docker, plain binary) are updated by
hand; they only get told.

## Shape

**Release source.** GitHub's releases API for `queball1999/meal-planner`
(public as of today). `/releases/latest` returns the newest release that isn't
a draft or a prerelease. `vX.Y.Z-dev` tags are published as prereleases
(build_manager.yaml), so dev builds are never offered.

**The check lives in Go, not in QUpdateTool.** Servers need the "update
available" flag too, and they don't ship an updater. The check only reads
release metadata; it never downloads anything.

- Runs a minute after start, then every 12 hours; the result is kept in memory.
- `UPDATE_CHECK` setting (Settings → General, on/off, default on), read live
  each tick, so turning it off needs no restart. For offline or
  privacy-minded installs.
- Skipped for builds that aren't a release version (`dev`, `dev-abc1234`):
  there is nothing to compare.
- `v0.0.2-dev` counts as older than `v0.0.2` (semver prerelease order).

**Where it shows.** Admins only: they're the ones who can act on it, and the
About page is admin-only already.

- Footer: `v0.0.1 (update available)`. On a server it links to the GitHub
  release page; on desktop it links to About → Updates, where Install lives.
- About → Updates card: this version, latest version and date, release notes
  link, when it last checked or why it failed, and Check now.
- About → Background processes: an "Update check" row.

**Installing (desktop only, commits 2-3).** The Tauri shell already knows how
it was installed: `portable.txt` beside the exe, or the `APPIMAGE` environment
variable on Linux. It passes the updater's path, the install kind and its own
process ID to the Go sidecar. The page still gets no Tauri capabilities. Go's
`POST /about/update` (admin, CSRF) starts the branded updater. The updater
stops the app, installs and relaunches it.

| Install | Asset | How QUpdateTool installs it |
|---|---|---|
| Windows installer | `GoEat-*-windows-x64-setup.exe` | NSIS detected, run silently with `/S`; per-user, no admin prompt |
| Windows portable | `GoEat-*-windows-x64-portable.zip` (`--asset-pattern`) | Unzipped over the exe folder with rollback; `data/` and `portable.txt` survive |
| Linux .deb | `GoEat-*-linux-amd64.deb` | Package manager, asks for admin rights |
| Linux AppImage | `GoEat-*-linux-amd64.AppImage` | Replaces the file at `$APPIMAGE` |

`server` assets are excluded so a Windows zip fallback never picks the server
build. The updater verifies `SHA256SUMS.txt` against the pinned release key
before installing anything.

## Commits

1. **Update check** — `updatecheck` package (GitHub latest release, semver
   compare, in-memory status, 12-hour loop), `UPDATE_CHECK` setting, footer
   flag, About → Updates card with Check now (`POST /about/update-check`,
   admin), background-process row. Tests against a fake GitHub. Status: done
2. **Branded updater in the desktop build** — `desktop/updater-brand.yaml`
   (app name, repo, `exclude_pattern: server`, pinned key fingerprints), a CI
   step that builds the branded updater from QUpdateTool for Windows and
   Linux, bundled as a Tauri resource; its SHA-256 is stamped into the Go
   sidecar with `-ldflags` and checked before every launch (QUpdateTool
   SECURITY.md). `tauri.conf.json` / `Cargo.toml` versions stamped from the
   tag so the installer and the footer agree. Status: planned
3. **Install from the app** — the shell passes `GOEAT_UPDATER`,
   `GOEAT_INSTALL_KIND` (installer | portable | deb | appimage) and
   `GOEAT_SHELL_PID` (plus `APPIMAGE`). `POST /about/update` (admin, desktop
   only) checks the updater hash, copies it to a temp folder (so a portable
   update can replace the original), and starts it with `--gui
   --calling-pid --executable --current-version --log-file <data>/logs`,
   adding `--asset-pattern` for portable. The About page gets an Install
   button. Status: planned
