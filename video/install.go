package video

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Installable components, in install order.
const (
	CompYtDlp   = "yt-dlp"
	CompFFmpeg  = "ffmpeg"
	CompWhisper = "whisper"
	CompModel   = "model"
)

// Components is every installable component, in install order.
var Components = []string{CompYtDlp, CompFFmpeg, CompWhisper, CompModel}

// asset is one file the installer downloads. Everything except yt-dlp is
// pinned here by URL and SHA-256 - an admin can't point the installer at
// anything else, and a tampered or truncated download is refused.
type asset struct {
	URL     string
	SHA256  string
	Size    int64
	Format  string // "raw" | "gz" | "zip" | "tgz"
	Version string
}

// whisper.cpp release b5130 (v1.9.4). CPU builds: they run anywhere, and a
// short recipe video transcribes in a few seconds without a GPU.
var whisperAssets = map[string]asset{
	"windows/amd64": {
		URL:    "https://github.com/ggml-org/whisper.cpp/releases/download/b5130/whisper-bin-x64.zip",
		SHA256: "f9ec6c52a2e949b62ab51fa21d0d497958f9e41c3010c157c4e42932d5316f3c",
		Size:   8573270, Format: "zip", Version: "1.9.4 (b5130)",
	},
	"linux/amd64": {
		URL:    "https://github.com/ggml-org/whisper.cpp/releases/download/b5130/whisper-bin-ubuntu-x64.tar.gz",
		SHA256: "53e7fd8b5764edad916b8848dd0af6abb1ff1d3b86c899e79c78652412536c32",
		Size:   9793438, Format: "tgz", Version: "1.9.4 (b5130)",
	},
	"linux/arm64": {
		URL:    "https://github.com/ggml-org/whisper.cpp/releases/download/b5130/whisper-bin-ubuntu-arm64.tar.gz",
		SHA256: "93532a0e3777f26f041ffa358ee77dd88b1a33a86847c1990745327ff335a5d6",
		Size:   4605905, Format: "tgz", Version: "1.9.4 (b5130)",
	},
}

// ffmpeg: eugeneware/ffmpeg-static b6.1.1 - one static binary per platform,
// ~30 MB gzipped, against ~200 MB for a full FFmpeg build. Only used to
// convert audio, so the small build is plenty.
var ffmpegAssets = map[string]asset{
	"windows/amd64": {
		URL:    "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/ffmpeg-win32-x64.gz",
		SHA256: "8883a3dffbd0a16cf4ef95206ea05283f78908dbfb118f73c83f4951dcc06d77",
		Size:   29581307, Format: "gz", Version: "6.1.1",
	},
	"linux/amd64": {
		URL:    "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/ffmpeg-linux-x64.gz",
		SHA256: "bfe8a8fc511530457b528c48d77b5737527b504a3797a9bc4866aeca69c2dffa",
		Size:   29354986, Format: "gz", Version: "6.1.1",
	},
	"linux/arm64": {
		URL:    "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/ffmpeg-linux-arm64.gz",
		SHA256: "754a678672298bc68156adff58aa7385a592c2b30b1d0ae8750c45c915c4bac0",
		Size:   25568691, Format: "gz", Version: "6.1.1",
	},
}

// ytDlpAssetNames is yt-dlp's standalone build per platform. Not pinned:
// TikTok and Instagram change often enough that a months-old yt-dlp stops
// working, so Update always fetches the latest release - verified against
// the SHA-256 GitHub publishes for that release asset.
var ytDlpAssetNames = map[string]string{
	"windows/amd64": "yt-dlp.exe",
	"linux/amd64":   "yt-dlp_linux",
	"linux/arm64":   "yt-dlp_linux_aarch64",
}

// approxSizes is shown before a download starts (yt-dlp's real size is only
// known once the latest release has been looked up).
var approxSizes = map[string]int64{
	"windows/amd64": 18 << 20,
	"linux/amd64":   40 << 20,
	"linux/arm64":   40 << 20,
}

// ModelInfo describes one Whisper model the installer offers.
type ModelInfo struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Note   string `json:"note"`
	Size   int64  `json:"size"`
	SHA256 string `json:"-"`
}

// Models are the Whisper models the installer offers, smallest first. From
// huggingface.co/ggerganov/whisper.cpp, pinned by SHA-256.
var Models = []ModelInfo{
	{"tiny.en", "Tiny (English)", "Fastest, makes more mistakes", 77704715, "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f"},
	{"base.en", "Base (English)", "Recommended - a short video in a few seconds", 147964211, "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002"},
	{"small.en", "Small (English)", "More accurate, about 3x slower", 487614201, "c6138d6d58ecc8322097e0f987c32f1be8bb0a18532a3f88f734d1bbf9c41e5d"},
	{"base", "Base (any language)", "For videos not in English", 147951465, "60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe"},
	{"small", "Small (any language)", "Any language, more accurate, slower", 487601967, "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b"},
}

// DefaultModel is what "Download everything" installs.
const DefaultModel = "base.en"

func modelInfo(name string) (ModelInfo, bool) {
	for _, m := range Models {
		if m.Name == name {
			return m, true
		}
	}
	return ModelInfo{}, false
}

func platformKey() string { return runtime.GOOS + "/" + runtime.GOARCH }

// isMusl reports a musl libc system (Alpine, i.e. the Docker image), where
// the glibc builds of yt-dlp and whisper.cpp won't start.
func isMusl() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	m, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	return len(m) > 0
}

// CanInstall reports whether the installer has a download of component for
// this machine. Where it doesn't, the tool has to come from the system
// package manager (it is still found on PATH).
func CanInstall(component string) bool {
	key := platformKey()
	switch component {
	case CompYtDlp:
		_, ok := ytDlpAssetNames[key]
		return ok
	case CompFFmpeg:
		_, ok := ffmpegAssets[key]
		return ok
	case CompWhisper:
		_, ok := whisperAssets[key]
		return ok && !isMusl()
	case CompModel:
		return true
	}
	return false
}

// DownloadSize is roughly how much installing component downloads.
func DownloadSize(component, model string) int64 {
	key := platformKey()
	switch component {
	case CompYtDlp:
		return approxSizes[key]
	case CompFFmpeg:
		return ffmpegAssets[key].Size
	case CompWhisper:
		return whisperAssets[key].Size
	case CompModel:
		m, _ := modelInfo(model)
		return m.Size
	}
	return 0
}

// githubAPI is swapped out in tests.
var githubAPI = "https://api.github.com"

// latestYtDlp looks up the newest yt-dlp release's asset for this machine.
func latestYtDlp(ctx context.Context, client *http.Client) (asset, error) {
	name, ok := ytDlpAssetNames[platformKey()]
	if !ok {
		return asset{}, fmt.Errorf("no yt-dlp download for %s", platformKey())
	}
	if isMusl() {
		name = strings.Replace(name, "yt-dlp_linux", "yt-dlp_musllinux", 1)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+"/repos/yt-dlp/yt-dlp/releases/latest", nil)
	if err != nil {
		return asset{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return asset{}, fmt.Errorf("look up the latest yt-dlp: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return asset{}, fmt.Errorf("look up the latest yt-dlp: GitHub answered %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
			URL    string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return asset{}, fmt.Errorf("look up the latest yt-dlp: %w", err)
	}
	for _, a := range rel.Assets {
		if a.Name != name {
			continue
		}
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || len(sum) != 64 {
			return asset{}, fmt.Errorf("yt-dlp %s: GitHub published no SHA-256 for %s", rel.TagName, name)
		}
		if !strings.HasPrefix(a.URL, "https://github.com/yt-dlp/yt-dlp/releases/download/") {
			return asset{}, fmt.Errorf("yt-dlp %s: unexpected download URL %q", rel.TagName, a.URL)
		}
		return asset{URL: a.URL, SHA256: sum, Size: a.Size, Format: "raw", Version: rel.TagName}, nil
	}
	return asset{}, fmt.Errorf("yt-dlp %s has no %s download", rel.TagName, name)
}

// download fetches a.URL into dst, hashing as it goes, and fails unless the
// SHA-256 matches. onProgress gets the bytes written so far.
func download(ctx context.Context, client *http.Client, a asset, dst string, onProgress func(int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", filepath.Base(a.URL), resp.Status)
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	// Cap at the expected size (plus slack for a size we only estimated), so
	// a wrong or hostile response can't fill the disk.
	limit := a.Size + 1<<20
	if a.Size <= 0 {
		limit = 2 << 30
	}
	pw := &progressWriter{onProgress: onProgress}
	n, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return fmt.Errorf("download %s: %w", filepath.Base(a.URL), err)
	}
	if n > limit {
		os.Remove(dst)
		return fmt.Errorf("download %s: larger than expected", filepath.Base(a.URL))
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		os.Remove(dst)
		return fmt.Errorf("download %s: checksum mismatch - the file was corrupted or tampered with, and was deleted", filepath.Base(a.URL))
	}
	return nil
}

type progressWriter struct {
	n          int64
	last       time.Time
	onProgress func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if p.onProgress != nil && time.Since(p.last) > 200*time.Millisecond {
		p.last = time.Now()
		p.onProgress(p.n)
	}
	return len(b), nil
}

// exeName adds .exe on Windows.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// unpack turns a downloaded, verified file into the component's files in
// dir. Archive entries are flattened to their base name and only the files
// the tool needs are kept, so nothing in an archive can write outside dir.
func unpack(a asset, src, dir, exe string) error {
	switch a.Format {
	case "raw":
		return copyFile(src, filepath.Join(dir, exe), 0o755)
	case "gz":
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		zr, err := gzip.NewReader(in)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(dir, exe), io.LimitReader(zr, 1<<30), 0o755)
	case "zip":
		zr, err := zip.OpenReader(src)
		if err != nil {
			return err
		}
		defer zr.Close()
		found := false
		for _, f := range zr.File {
			base := filepath.Base(filepath.FromSlash(f.Name))
			if f.FileInfo().IsDir() || !keepWhisperFile(base) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			err = writeFile(filepath.Join(dir, base), io.LimitReader(rc, 512<<20), 0o755)
			rc.Close()
			if err != nil {
				return err
			}
			found = found || base == exe
		}
		if !found {
			return fmt.Errorf("%s not found in the download", exe)
		}
		return nil
	case "tgz":
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		gz, err := gzip.NewReader(in)
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		found := false
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			base := filepath.Base(h.Name)
			if !keepWhisperFile(base) {
				continue
			}
			switch h.Typeflag {
			case tar.TypeReg:
				if err := writeFile(filepath.Join(dir, base), io.LimitReader(tr, 512<<20), 0o755); err != nil {
					return err
				}
				found = found || base == exe
			case tar.TypeSymlink:
				// The shared libraries are versioned symlinks
				// (libwhisper.so.1 -> libwhisper.so.1.9.4); keep the ones
				// that point at a sibling file.
				if strings.ContainsAny(h.Linkname, `/\`) || !keepWhisperFile(h.Linkname) {
					continue
				}
				if err := os.Symlink(h.Linkname, filepath.Join(dir, base)); err != nil {
					return err
				}
			}
		}
		if !found {
			return fmt.Errorf("%s not found in the download", exe)
		}
		return nil
	}
	return fmt.Errorf("unknown download format %q", a.Format)
}

// keepWhisperFile picks whisper-cli and the libraries it loads out of a
// whisper.cpp release, which also carries a dozen other example programs.
func keepWhisperFile(base string) bool {
	switch {
	case base == "whisper-cli" || base == "whisper-cli.exe":
		return true
	case strings.HasSuffix(strings.ToLower(base), ".dll"):
		return true
	case strings.HasPrefix(base, "lib") && strings.Contains(base, ".so"):
		return true
	}
	return false
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFile(dst, in, mode)
}

func writeFile(dst string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// swapDir puts staging in place of dir, keeping the old dir until the new
// one is in place so a failed swap leaves a working install.
func swapDir(staging, dir string) error {
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return fmt.Errorf("replace %s (is it running?): %w", filepath.Base(dir), err)
		}
	}
	if err := os.Rename(staging, dir); err != nil {
		_ = os.Rename(old, dir)
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

// versionFile records what the installer put in a component's folder.
const versionFile = "VERSION"

// InstalledVersion is the version the installer recorded for component, or
// "" when it wasn't installed by the installer.
func (t Tools) InstalledVersion(component string) string {
	if t.Dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(t.Dir, component, versionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// activeModelFile names the model transcription uses, inside ModelsDir.
const activeModelFile = "active"

// ActiveModel is the model chosen in Settings, or "" for the default pick.
func (t Tools) ActiveModel() string {
	if dir := t.ModelsDir(); dir != "" {
		b, err := os.ReadFile(filepath.Join(dir, activeModelFile))
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// SetActiveModel makes an installed model the one transcription uses.
func (t Tools) SetActiveModel(name string) error {
	if _, ok := modelInfo(name); !ok {
		return fmt.Errorf("unknown model %q", name)
	}
	dir := t.ModelsDir()
	if dir == "" || !isFile(filepath.Join(dir, ModelFile(name))) {
		return fmt.Errorf("model %s is not installed", name)
	}
	return os.WriteFile(filepath.Join(dir, activeModelFile), []byte(name+"\n"), 0o644)
}

// InstalledModels lists the installer's models present in ModelsDir.
func (t Tools) InstalledModels() []string {
	var out []string
	for _, m := range Models {
		if dir := t.ModelsDir(); dir != "" && isFile(filepath.Join(dir, ModelFile(m.Name))) {
			out = append(out, m.Name)
		}
	}
	return out
}

// install downloads, verifies and unpacks one component. progress gets
// (state, bytes done, bytes total).
func (t Tools) install(ctx context.Context, client *http.Client, component, model string, progress func(string, int64, int64)) (string, error) {
	if t.Dir == "" {
		return "", errors.New("TOOLS_DIR is not set")
	}
	if !CanInstall(component) {
		if component == CompWhisper && isMusl() {
			return "", errors.New("whisper.cpp has no build for Alpine (musl) - use a whisper.cpp server instead (in Docker, the whisper container)")
		}
		return "", fmt.Errorf("no %s download for %s - install it with your system's package manager", component, platformKey())
	}
	var a asset
	switch component {
	case CompYtDlp:
		progress("downloading", 0, 0)
		var err error
		if a, err = latestYtDlp(ctx, client); err != nil {
			return "", err
		}
	case CompFFmpeg:
		a = ffmpegAssets[platformKey()]
	case CompWhisper:
		a = whisperAssets[platformKey()]
	case CompModel:
		m, ok := modelInfo(model)
		if !ok {
			return "", fmt.Errorf("unknown model %q", model)
		}
		a = asset{
			URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" + ModelFile(m.Name),
			SHA256: m.SHA256, Size: m.Size, Format: "raw", Version: m.Name,
		}
	default:
		return "", fmt.Errorf("unknown component %q", component)
	}

	downloads := filepath.Join(t.Dir, ".downloads")
	part := filepath.Join(downloads, component+".part")
	if component == CompModel {
		// A model downloads next to where it ends up, under a dot-name, and
		// is renamed into place only once complete and verified: the models
		// folder can be its own volume (Docker's /models), where a rename
		// from TOOLS_DIR would cross filesystems, and the whisper sidecar
		// starts the moment a ggml-*.bin appears - it must never see a
		// half-written one.
		downloads = t.ModelsDir()
		part = filepath.Join(downloads, "."+ModelFile(model)+".part")
	}
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return "", err
	}
	defer os.Remove(part)
	progress("downloading", 0, a.Size)
	if err := download(ctx, client, a, part, func(n int64) { progress("downloading", n, a.Size) }); err != nil {
		return "", err
	}
	progress("installing", a.Size, a.Size)

	if component == CompModel {
		dir := t.ModelsDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(part, filepath.Join(dir, ModelFile(model))); err != nil {
			return "", err
		}
		return a.Version, t.UseModel(ctx, model)
	}

	exe := map[string]string{CompYtDlp: exeName(ToolYtDlp), CompFFmpeg: exeName(ToolFFmpeg), CompWhisper: exeName(ToolWhisper)}[component]
	staging := filepath.Join(t.Dir, "."+component+".new")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	if err := unpack(a, part, staging, exe); err != nil {
		os.RemoveAll(staging)
		return "", fmt.Errorf("unpack %s: %w", component, err)
	}
	_ = os.WriteFile(filepath.Join(staging, versionFile), []byte(a.Version+"\n"), 0o644)
	if err := swapDir(staging, filepath.Join(t.Dir, component)); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	return a.Version, nil
}

// Remove deletes a component the installer downloaded (a model: the active
// one). Tools on the system PATH are never touched.
func (t Tools) Remove(component string) error {
	if t.Dir == "" {
		return errors.New("TOOLS_DIR is not set")
	}
	switch component {
	case CompYtDlp, CompFFmpeg, CompWhisper:
		return os.RemoveAll(filepath.Join(t.Dir, component))
	}
	return fmt.Errorf("unknown component %q", component)
}

// RemoveModel deletes one downloaded model.
func (t Tools) RemoveModel(name string) error {
	if _, ok := modelInfo(name); !ok {
		return fmt.Errorf("unknown model %q", name)
	}
	dir := t.ModelsDir()
	if dir == "" {
		return errors.New("TOOLS_DIR is not set")
	}
	if t.ActiveModel() == name {
		_ = os.Remove(filepath.Join(dir, activeModelFile))
	}
	err := os.Remove(filepath.Join(dir, ModelFile(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
