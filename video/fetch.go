package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// MaxDurationSeconds is the longest video Fetch accepts. Recipe shorts are
// under three minutes; this leaves room for a full YouTube recipe video
// while stopping someone pasting a two-hour livestream.
const MaxDurationSeconds = 15 * 60

// maxComments caps how many creator comments are kept, and maxCommentLen how
// much of each - a pinned "full recipe 👇" comment is a few hundred
// characters; anything far longer is not a recipe.
const (
	maxComments   = 10
	maxCommentLen = 3000
)

// Media is what Fetch read from a video post.
type Media struct {
	URL          string // canonical post URL (yt-dlp's webpage_url)
	Platform     string // "TikTok" | "Instagram" | "YouTube"
	Title        string
	Caption      string // the post's description text
	Uploader     string
	Duration     int // seconds; 0 when unknown
	ThumbnailURL string
	// Comments are the creator's own and pinned comments - where "recipe in
	// the comments" puts it. Other people's comments are dropped: they are
	// noise at best and prompt injection at worst.
	Comments []string
	// AudioPath is the 16 kHz mono WAV of the audio track, inside the work
	// dir Fetch was given; "" when the post has no audio.
	AudioPath string
}

// Fetch reads the post at rawURL with yt-dlp into workDir: its metadata
// (caption, uploader, thumbnail, comments) and its audio as a WAV ready for
// Transcribe. workDir must exist; the caller removes it afterwards.
func Fetch(ctx context.Context, t Tools, rawURL, workDir string) (*Media, error) {
	platform := Platform(rawURL)
	if platform == "" {
		return nil, fmt.Errorf("video: %q is not a TikTok, Instagram or YouTube link", rawURL)
	}
	ytdlp, err := t.Find(ToolYtDlp)
	if err != nil {
		return nil, err
	}
	ffmpeg, err := t.Find(ToolFFmpeg)
	if err != nil {
		return nil, err
	}

	if _, err := run(ctx, ytdlp, fetchArgs(rawURL, ffmpeg, workDir)...); err != nil {
		return nil, fmt.Errorf("video: read %s post: %w", platform, err)
	}

	info, err := os.ReadFile(filepath.Join(workDir, "media.info.json"))
	if errors.Is(err, os.ErrNotExist) {
		// yt-dlp exits 0 without writing anything when --match-filter
		// rejects the post.
		return nil, fmt.Errorf("video: skipped - longer than %d minutes, a live stream, or not a video", MaxDurationSeconds/60)
	}
	if err != nil {
		return nil, fmt.Errorf("video: %w", err)
	}
	m, err := parseInfo(info)
	if err != nil {
		return nil, err
	}
	m.Platform = platform
	if m.URL == "" {
		m.URL = rawURL
	}
	if wav := filepath.Join(workDir, "media.wav"); isFile(wav) {
		m.AudioPath = wav
	}
	return m, nil
}

// fetchArgs is the yt-dlp command line for Fetch.
//
// The flags that matter for safety: --ignore-config so a stray yt-dlp.conf
// on the host can't change any of this; --ies default,-generic so only the
// real site extractors run (the generic one would fetch any page it was
// pointed at); and "--" before the URL so it can never be read as a flag.
func fetchArgs(rawURL, ffmpeg, workDir string) []string {
	return []string{
		"--ignore-config",
		"--no-playlist",
		"--no-progress",
		"--no-update",
		"--quiet",
		"--ies", "default,-generic",
		"--match-filter", fmt.Sprintf("!is_live & duration <=? %d", MaxDurationSeconds),
		"--max-filesize", "200M",
		"--ffmpeg-location", ffmpeg,
		"--write-info-json",
		"--write-comments",
		// YouTube pages through every comment by default; the first page
		// of top-level comments is where a pinned recipe would be.
		"--extractor-args", "youtube:max_comments=40,40,0,0",
		"-f", "ba/b",
		"-x", "--audio-format", "wav",
		"--postprocessor-args", "ExtractAudio:-ar 16000 -ac 1",
		"-o", filepath.Join(workDir, "media.%(ext)s"),
		"--", rawURL,
	}
}

// ytInfo is the subset of yt-dlp's info JSON Fetch reads.
type ytInfo struct {
	WebpageURL  string  `json:"webpage_url"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Uploader    string  `json:"uploader"`
	Channel     string  `json:"channel"`
	Duration    float64 `json:"duration"`
	Thumbnail   string  `json:"thumbnail"`
	Comments    []struct {
		Text             string `json:"text"`
		AuthorIsUploader bool   `json:"author_is_uploader"`
		IsPinned         bool   `json:"is_pinned"`
	} `json:"comments"`
}

func parseInfo(data []byte) (*Media, error) {
	var in ytInfo
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("video: read yt-dlp info: %w", err)
	}
	m := &Media{
		URL:          in.WebpageURL,
		Title:        strings.TrimSpace(in.Title),
		Caption:      strings.TrimSpace(in.Description),
		Uploader:     strings.TrimSpace(in.Uploader),
		Duration:     int(in.Duration),
		ThumbnailURL: in.Thumbnail,
	}
	if m.Uploader == "" {
		m.Uploader = strings.TrimSpace(in.Channel)
	}
	for _, c := range in.Comments {
		text := strings.TrimSpace(c.Text)
		if text == "" || !(c.AuthorIsUploader || c.IsPinned) {
			continue
		}
		if len(text) > maxCommentLen {
			text = text[:maxCommentLen]
		}
		m.Comments = append(m.Comments, text)
		if len(m.Comments) == maxComments {
			break
		}
	}
	return m, nil
}

// run executes a tool and returns its stdout. A failure's error carries the
// tool's own last error line, which is what tells the user what went wrong
// ("ERROR: [TikTok] 123: Video not available").
func run(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := command(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if msg := lastErrorLine(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return stdout.Bytes(), nil
}

// command builds the exec.Cmd for a tool. On Linux the tool's own folder
// goes on LD_LIBRARY_PATH: whisper.cpp's release ships its shared libraries
// beside whisper-cli rather than installing them system-wide.
func command(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	hideWindow(cmd)
	if runtime.GOOS == "linux" {
		lib := filepath.Dir(path)
		if v := os.Getenv("LD_LIBRARY_PATH"); v != "" {
			lib += ":" + v
		}
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+lib)
	}
	return cmd
}

// lastErrorLine picks the most useful line out of a tool's stderr: the last
// "ERROR:" line if there is one (yt-dlp), else the last non-empty line.
func lastErrorLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "ERROR:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "ERROR:"))
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
