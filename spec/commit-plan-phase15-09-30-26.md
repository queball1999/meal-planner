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
   (app name, repo, release-tag pattern, `exclude_pattern: server`, pinned
   key fingerprints) and `desktop/gpg-public.asc` (the release key, shared
   with QSnippet). `build_updater.yaml` builds the GUI updater from
   QUpdateTool2.0, pinned to a commit, on each OS before the desktop build.
   The Makefile bundles it as a Tauri resource (and in the portable zip) when
   present, so local builds without Python still work. `cmd/build_sidecar`
   stamps its SHA-256 into the sidecar (`main.updaterSHA256`); About →
   Updates shows it on desktop. Status: done

   Dropped from this commit: version stamping. CI already passes the tag's
   version to `tauri build --config`, so the installer and the footer agree;
   `tauri.conf.json`'s 0.1.0 is only the local default.

   Process names list the desktop shell only, never `goeat`. QUpdateTool
   matches names machine-wide, and a root `.deb` update would otherwise stop
   a Go Eat server on the same machine.

   Checked locally against the published v0.0.2: the branded build picks
   `GoEat-0.0.2-windows-x64-setup.exe`, or the portable zip with
   `--asset-pattern`, never a server build. `--dry-run` downloads it, matches
   SHA256SUMS.txt and verifies the signature with the pinned key.
3. **Install from the app** — the shell passes `GOEAT_UPDATER`,
   `GOEAT_INSTALL_KIND` (installer | portable | deb | appimage) and
   `GOEAT_SHELL_PID` (plus `APPIMAGE`). `POST /about/update` (admin, desktop
   only) checks the updater hash, copies it to a temp folder (so a portable
   update can replace the original), and starts it with `--gui
   --calling-pid --executable --current-version --log-file <data>/logs`,
   adding `--asset-pattern` for portable. The About page gets an Install
   button. Status: done

   The copy is hashed, not the original, so the file can't be swapped
   between the check and the launch; running from temp also lets a portable
   update replace the bundled updater.exe and gets an AppImage's updater out
   of its read-only mount. The updater starts detached (new process group on
   Windows, its own session on Linux) so it outlives the shell it stops.
   Install says why when it can't be offered (dev build, no bundled updater,
   unknown install kind, already up to date), and a second click within two
   minutes is ignored. Starts and failures go to the audit log.

   Checked end to end on Windows: the branded updater, run with the
   arguments Go builds for a portable install, stopped the stand-in process
   by PID, downloaded the published v0.0.2 portable zip, verified it, and
   unpacked it over the folder with `data/` and `portable.txt` intact.
