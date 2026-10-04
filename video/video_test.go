package video

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPlatform(t *testing.T) {
	cases := map[string]string{
		"https://www.tiktok.com/@nytcooking/video/7691814905363614989": "TikTok",
		"https://vm.tiktok.com/ZMabc123/":                              "TikTok",
		"https://www.tiktok.com/t/ZT8abc/":                             "TikTok",
		"https://www.instagram.com/reel/C1abcDEF/":                     "Instagram",
		"https://instagram.com/p/C1abcDEF/":                            "Instagram",
		"https://www.youtube.com/shorts/abcdEFGhij":                    "YouTube",
		"https://m.youtube.com/watch?v=abcdEFGhij":                     "YouTube",
		"https://youtu.be/abcdEFGhij":                                  "YouTube",

		// Not video posts, or not these platforms.
		"https://www.tiktok.com/@nytcooking":             "",
		"https://www.instagram.com/nytcooking/":          "",
		"https://www.youtube.com/@nytcooking":            "",
		"https://www.youtube.com/watch":                  "",
		"https://www.allrecipes.com/recipe/1/pasta":      "",
		"https://tiktok.com.evil.example/video/1":        "",
		"https://user:pw@www.tiktok.com/@a/video/1":      "",
		"file:///etc/passwd":                             "",
		"http://169.254.169.254/latest/meta-data/":       "",
		"ftp://www.youtube.com/shorts/abcdEFGhij":        "",
		"https://www.tiktok.com.attacker.net/@a/video/1": "",
	}
	for in, want := range cases {
		if got := Platform(in); got != want {
			t.Errorf("Platform(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchArgsKeepsURLAfterDoubleDash(t *testing.T) {
	args := fetchArgs("https://youtu.be/--exec=calc", "/x/ffmpeg", "/tmp/w")
	n := len(args)
	if args[n-2] != "--" || args[n-1] != "https://youtu.be/--exec=calc" {
		t.Fatalf("URL must be the last arg, after --; got %q", args[n-2:])
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--ignore-config", "--ies default,-generic", "--no-playlist"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q", want)
		}
	}
}

func TestParseInfoKeepsOnlyCreatorComments(t *testing.T) {
	info := `{
		"webpage_url": "https://www.youtube.com/shorts/abc",
		"title": "Lemon pasta",
		"description": "  Full recipe below  ",
		"uploader": "",
		"channel": "Chef Sam",
		"duration": 42.6,
		"thumbnail": "https://i.ytimg.com/vi/abc/hq.jpg",
		"comments": [
			{"text": "looks great", "author_is_uploader": false, "is_pinned": false},
			{"text": "Recipe: 200g pasta, 1 lemon", "author_is_uploader": true, "is_pinned": false},
			{"text": "Pinned: serves 2", "author_is_uploader": false, "is_pinned": true},
			{"text": "   ", "author_is_uploader": true}
		]
	}`
	m, err := parseInfo([]byte(info))
	if err != nil {
		t.Fatal(err)
	}
	if m.Caption != "Full recipe below" || m.Uploader != "Chef Sam" || m.Duration != 42 {
		t.Errorf("got caption %q uploader %q duration %d", m.Caption, m.Uploader, m.Duration)
	}
	if len(m.Comments) != 2 || m.Comments[0] != "Recipe: 200g pasta, 1 lemon" || m.Comments[1] != "Pinned: serves 2" {
		t.Errorf("comments = %q", m.Comments)
	}
}

func TestLastErrorLine(t *testing.T) {
	stderr := "WARNING: something\nERROR: [TikTok] 123: Video not available\nsome trailer\n"
	if got := lastErrorLine(stderr); got != "[TikTok] 123: Video not available" {
		t.Errorf("got %q", got)
	}
	if got := lastErrorLine("first\nlast line\n\n"); got != "last line" {
		t.Errorf("got %q", got)
	}
}

func TestToolsFindPrefersToolsDir(t *testing.T) {
	dir := t.TempDir()
	exe := ToolWhisper
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	want := filepath.Join(dir, "whisper", "Release", exe)
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Tools{Dir: dir}.Find(ToolWhisper)
	if err != nil || got != want {
		t.Fatalf("Find = %q, %v; want %q", got, err, want)
	}
}

func TestModelPathPreference(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	_ = os.MkdirAll(models, 0o755)

	if _, _, err := (Tools{Dir: dir}).ModelPath(); err == nil {
		t.Fatal("expected an error with no models installed")
	}
	for _, n := range []string{"small", "tiny.en"} {
		_ = os.WriteFile(filepath.Join(models, ModelFile(n)), []byte("x"), 0o644)
	}
	if _, name, _ := (Tools{Dir: dir}).ModelPath(); name != "tiny.en" {
		t.Errorf("default pick = %q, want tiny.en (English-only first)", name)
	}
	if _, name, _ := (Tools{Dir: dir, Model: "small"}).ModelPath(); name != "small" {
		t.Errorf("chosen pick = %q, want small", name)
	}
	if _, name, _ := (Tools{Dir: dir, Model: "large"}).ModelPath(); name != "tiny.en" {
		t.Errorf("missing choice should fall back; got %q", name)
	}
}

// TestFetchAndTranscribeLive runs the real tools against a real post:
//
//	GOEAT_VIDEO_IT=https://www.tiktok.com/@nytcooking/video/7691814905363614989 \
//	GOEAT_TOOLS_DIR=../data/tools go test ./video -run Live -v
func TestFetchAndTranscribeLive(t *testing.T) {
	url := os.Getenv("GOEAT_VIDEO_IT")
	if url == "" {
		t.Skip("set GOEAT_VIDEO_IT to a video URL to run")
	}
	tools := Tools{Dir: os.Getenv("GOEAT_TOOLS_DIR")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	work := t.TempDir()
	m, err := Fetch(ctx, tools, url, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s by %s, %ds\ncaption: %s\ncomments: %q", m.Platform, m.Uploader, m.Duration, m.Caption, m.Comments)
	if m.AudioPath == "" {
		t.Fatal("no audio")
	}
	text, model, err := Transcribe(ctx, tools, m.AudioPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transcript (%s): %s", model, text)
	if text == "" {
		t.Error("empty transcript")
	}
}
