# Phase 16 — Import recipes from short videos (2026-10-02)

Goal: paste a TikTok / Instagram Reel / YouTube Shorts link into Import Recipe
and get a structured recipe back, the way reelrecipes.recipes does it.
Local tools first (yt-dlp, ffmpeg, whisper.cpp on this machine); cloud
transcription (OpenAI-compatible `/audio/transcriptions`, Gemini) comes after
the local proof of concept is verified.

## How it works

1. **yt-dlp** reads the post: caption, uploader, thumbnail, comments, and the
   audio track. `--ffmpeg-location` points it at our ffmpeg, which converts the
   audio to 16 kHz mono WAV (the only input whisper.cpp reads reliably; its
   built-in decoder can't read TikTok's AAC).
2. **whisper.cpp** (`whisper-cli`) transcribes the WAV.
3. **The configured LLM** gets caption + the creator's own comments +
   transcript and returns recipe JSON, or `found: false` when the video
   doesn't contain one (common: "recipe at the link in bio").
4. The result goes through the same save path as a web import.

Manual proof of concept (2026-10-02, this machine, CPU only), one 31 s
@nytcooking TikTok: yt-dlp fetch + WAV conversion 3.8 s, whisper `base.en`
3.6 s. The transcript is noisy ("ground sugar", "salamander") - the prompt
must say so. yt-dlp returned 0 comments for that post, so comments are a
bonus, never required.

## Decisions

- **Hosts are allow-listed** (tiktok.com, vm/vt.tiktok.com, instagram.com
  reels/posts, youtube.com/shorts + watch, youtu.be). yt-dlp's generic
  extractor is disabled (`--ies default,-generic`) and the URL is passed after
  `--`, so a pasted link can neither reach internal hosts nor inject flags.
  `--ignore-config`, `--no-playlist`, a 15-minute duration cap and a 200 MB
  size cap keep one import bounded.
- **Tools are found in `TOOLS_DIR` first, then PATH.** `TOOLS_DIR` is
  env-only (like `DATABASE_URL`): it names directories we execute binaries
  from, so it is not editable from the web UI. Default `./data/tools`; on
  desktop `GOEAT_DATA_DIR/tools`.
- **Whisper is optional per import.** yt-dlp + ffmpeg are required; without
  whisper (or without a model) the import runs caption + comments only and
  says so on the progress screen.
- **Quantities.** The model must not invent ingredients. When a quantity isn't
  stated it may estimate a typical one, and the recipe gets an
  `estimated-amounts` tag so the user knows to check.
- **Background job + progress page**, reusing `plan.JobManager` (its own
  instance, so a video import and a plan generation don't block each other).
  Temp files live in one `os.MkdirTemp` dir, removed when the job ends.
- **Saved as `source_kind = 'imported'`**, `source_site` = the platform host,
  so every existing recipe page/template keeps working.
- **Nothing is shipped; tools are downloaded on demand (commit 4), only
  pinned or hash-checked files.** whisper.cpp b5130 (1.9.4) CPU builds,
  ffmpeg from eugeneware/ffmpeg-static b6.1.1 (one static binary, ~30 MB
  gzipped vs ~200 MB for a full build) and the Whisper models
  (huggingface ggerganov/whisper.cpp) are pinned by URL + SHA-256 in
  `video/install.go`. yt-dlp, which TikTok breakage forces us to update
  often, resolves the latest GitHub release and is verified against the
  `digest` GitHub publishes for that asset. No admin-supplied URLs. Archives
  are flattened to base names and filtered to the files the tool needs, so
  an entry can't write outside its folder. Platforms: windows/amd64,
  linux/amd64, linux/arm64 (musl gets yt-dlp's musllinux build; whisper.cpp
  has no musl build, so Alpine/Docker stays captions-only for now).

## Commits

1. **`video` package: tool lookup, yt-dlp fetch, whisper transcription.**
   `video.Tools` (TOOLS_DIR then PATH), `video.IsVideoURL`, `video.Fetch`
   (info JSON + WAV into a work dir; caption, uploader, thumbnail, creator /
   pinned comments), `video.Transcribe`. Tests for URL allow-list, info.json
   parsing, comment filtering, arg building; one integration test gated on
   `GOEAT_VIDEO_IT=<url>`.
   Status: done (uncommitted). Live test against the @nytcooking TikTok:
   fetch 3.3 s, transcribe 3.6-5.7 s. yt-dlp 2026.03's TikTok extractor
   doesn't fetch comments at all, so comments only come from YouTube/Instagram.

2. **LLM structuring + shared save path.** `recipes.StructureVideoRecipe`
   (prompt, JSON parse, `ErrNoRecipeInVideo`), `recipes.ImportVideo`
   (fetch → transcribe → structure → save, progress callback), `Import`'s
   persistence split into `save` so both paths share it. Tests with a fake
   generator.
   Status: done (uncommitted). Live run with qwen3.6-35b-a3b: 9.5 s, sensible
   recipe. Two fixes from that run: the first prompt let the model refuse
   when amounts were missing (now: never a reason to refuse); models return
   steps as objects and numbers for quantities (now: lenient flexText /
   flexInt), and sometimes leave the unit in the name (now split out).
   `cmd/probe_video <url>` runs the whole pipeline without saving.

3. **Import page: video links run as a background job.** POST
   `/recipes/import` routes video URLs to `s.videoJobs`; `GET
   /recipes/import/video` progress page (steps: Download → Transcribe → Ask AI
   → Save) streams `GET /recipes/import/video/status` SSE and redirects to the
   new recipe. Clear errors when no AI provider or no yt-dlp/ffmpeg.
   Status: done (uncommitted), unit-tested; not yet clicked through in the
   running app. Re-import on a video recipe says it isn't supported yet.

4. **Admin tool installer + import-page banner.** Settings → AI Setup (new
   tab holding the AI Provider card and a Video recipe import card): a 4-step walkthrough - AI provider, download
   the tools (one row each: status, where it came from, version,
   Download / Update / Remove, live progress), pick a speech model (Use /
   Remove / Download), Verify. Verify runs each tool and transcribes a
   second of silence with the model, so a binary that is present but can't
   start shows up. Import Recipe: a "What can I import?" section, and a
   banner when video import is unavailable (no AI provider / no yt-dlp +
   FFmpeg) or limited (no speech-to-text), with Download now for admins.
   Status: done (uncommitted). Live install into an empty folder with PATH
   cleared: yt-dlp 2026.08.19, ffmpeg 6.1.1, whisper.cpp 1.9.4, tiny.en -
   all downloaded, hash-checked and verified in 18 s. Settings card and
   banner not yet clicked through in the running app.

## Speech-to-text per install type (commits 5-6, added 2026-10-02)

| Install | yt-dlp + FFmpeg | Speech-to-text |
|---|---|---|
| Desktop (Windows / Linux) | downloaded from AI Setup into the app data folder | local whisper-cli, downloaded from AI Setup |
| Plain server binary (no Docker) | downloaded from AI Setup | local whisper-cli (glibc Linux, Windows), or point WHISPER_URL at any whisper.cpp server |
| Docker | downloaded from AI Setup into /data/tools (yt-dlp's musllinux build, static FFmpeg) | the `whisper` sidecar container, WHISPER_URL=http://whisper:8081 |

whisper.cpp has no musl build, so the Alpine app image can't run whisper-cli
itself; the sidecar is how Docker gets speech-to-text. Pattern from
QSS/audio-transcription-server (internal-only whisper-server, CPU only,
`--no-gpu`, audio POSTed to `/inference`), with three changes:

- **Model on a shared volume, downloaded from Go Eat.** whisper-server exits
  (code 3) if its model is missing at startup. The `whisper-models` volume is
  mounted at `/models` in both containers (WHISPER_MODELS_DIR=/models in the
  app); our image's entrypoint waits for a model, then starts the server.
  Same path in both containers, so "Use this" on a model can call the
  server's `POST /load` with the app's own path. Not nested under /data:
  Docker would create the nested mount point root-owned inside the existing
  goeat-data volume, and the app runs as an unprivileged user.
- **Portable CPU build.** ATS builds the image on the host (GGML_NATIVE=ON).
  We publish from CI, so native would bake in the runner's CPU and SIGILL
  elsewhere. Built with GGML_NATIVE=OFF + GGML_BACKEND_DL=ON +
  GGML_CPU_ALL_VARIANTS=ON: every CPU variant ships, the best one is picked at
  runtime (how upstream's own release zips are built).
- **Published image** `ghcr.io/<owner>/goeat-whisper`, amd64 on
  ubuntu-24.04 and arm64 on the native ubuntu-24.04-arm runner (C++ under QEMU
  is far too slow), merged into one multi-arch tag. Released with Go Eat.

WHISPER_URL is a Settings value (AI Setup card), read live like UPDATE_CHECK,
so desktop or plain-binary installs can also use a whisper server elsewhere
(a home server) instead of transcribing locally.

5. **whisper.cpp server backend.** `video.Tools.ServerURL`: Transcribe POSTs
   the WAV to `<url>/inference` instead of running whisper-cli; Capability
   probes `<url>/health` (cached briefly); Verify checks health and
   transcribes a second of silence through the server; SetActiveModel calls
   `/load`. WHISPER_URL + WHISPER_MODELS_DIR config, AI Setup card shows the
   server row and the URL field.
   Status: done (uncommitted). Unit-tested against a fake whisper-server;
   live: the @nytcooking TikTok transcribed through the real container in
   1.6 s (tiny.en). `/load` is only sent when WHISPER_MODELS_DIR is set
   (shared folder): whisper-server has a bug where a /load for a path it
   can't see leaves it reporting "loading model" until restarted.

6. **whisper image, CI, compose.** `whisper/Dockerfile` + `entrypoint.sh`,
   `.github/workflows/whisper_image.yaml` (called from build_manager),
   compose `whisper` service + `whisper-models` volume, app image gets
   TOOLS_DIR=/data/tools and an app-owned /models.
   Status: done (uncommitted); CI workflow not yet run. Local amd64 image:
   146 MB, ~90 s to build, waits with no model, picked the haswell CPU
   variant at runtime, switches models via /load. App + whisper together:
   unreachable until a model lands, healthy seconds after. Found and fixed
   on the way: models downloaded into TOOLS_DIR/.downloads and were renamed
   into /models - a cross-volume rename that fails in Docker, and the
   sidecar could start on a half-written file. Models now download as
   .ggml-<name>.bin.part inside the models folder. yt-dlp (musllinux) and
   the static FFmpeg verified running on Alpine.

Later (after the local path is verified): cloud transcription backends,
Docker image (`apk add yt-dlp ffmpeg`; upstream whisper Linux builds are
glibc, Alpine is musl), on-screen text from frames.
