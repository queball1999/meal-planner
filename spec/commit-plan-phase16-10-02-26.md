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

Later (after the local path is verified): cloud transcription backends,
Docker image (`apk add yt-dlp ffmpeg`; upstream whisper Linux builds are
glibc, Alpine is musl), on-screen text from frames.
