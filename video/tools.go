package video

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Tool names, as looked up by Tools.Find.
const (
	ToolYtDlp   = "yt-dlp"
	ToolFFmpeg  = "ffmpeg"
	ToolWhisper = "whisper-cli"
)

// Tools finds the external programs a video import runs.
//
// Each tool is looked for in its own folder under Dir first - where the
// Settings installer puts it - and then on PATH, so a machine that already
// has yt-dlp or ffmpeg installed (or a Docker image that apk-installs them)
// works without downloading anything.
type Tools struct {
	// Dir is TOOLS_DIR. "" means PATH only.
	Dir string
	// Model is the preferred Whisper model name ("base.en", "small", ...).
	// "" uses the one picked in Settings (ActiveModel), else the first
	// installed one in ModelPreference order.
	Model string
}

// toolDirs is where each tool lives under Tools.Dir. The extra
// subdirectories match how the upstream archives unpack, so a hand-extracted
// zip works too.
var toolDirs = map[string][]string{
	ToolYtDlp:   {"yt-dlp"},
	ToolFFmpeg:  {"ffmpeg", "ffmpeg/bin"},
	ToolWhisper: {"whisper", "whisper/Release", "whisper/bin", "whisper/build/bin"},
}

// ErrToolMissing is wrapped by Find when a tool is in neither place.
var ErrToolMissing = errors.New("not installed")

// Find returns the path of the named tool (ToolYtDlp, ToolFFmpeg,
// ToolWhisper).
func (t Tools) Find(name string) (string, error) {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if t.Dir != "" {
		for _, sub := range toolDirs[name] {
			p := filepath.Join(t.Dir, filepath.FromSlash(sub), exe)
			if isFile(p) {
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p, nil
	}
	return "", &MissingError{Tool: name}
}

// MissingError reports a tool that couldn't be found.
type MissingError struct{ Tool string }

func (e *MissingError) Error() string { return e.Tool + " is " + ErrToolMissing.Error() }
func (e *MissingError) Unwrap() error { return ErrToolMissing }

// ModelPreference is the order models are picked in when Tools.Model is
// unset: the English-only models are faster and more accurate for English,
// and base is the best speed/accuracy trade-off on a CPU.
var ModelPreference = []string{"base.en", "small.en", "tiny.en", "base", "small", "tiny"}

// ModelsDir is where Whisper models live: TOOLS_DIR/models.
func (t Tools) ModelsDir() string {
	if t.Dir == "" {
		return ""
	}
	return filepath.Join(t.Dir, "models")
}

// ModelPath returns the Whisper model to transcribe with and its name, or
// a *MissingError when none is installed.
func (t Tools) ModelPath() (path, name string, err error) {
	dir := t.ModelsDir()
	if dir == "" {
		return "", "", &MissingError{Tool: "Whisper model"}
	}
	names := ModelPreference
	chosen := t.Model
	if chosen == "" {
		chosen = t.ActiveModel()
	}
	if chosen != "" {
		names = append([]string{chosen}, ModelPreference...)
	}
	for _, n := range names {
		p := filepath.Join(dir, ModelFile(n))
		if isFile(p) {
			return p, n, nil
		}
	}
	return "", "", &MissingError{Tool: "Whisper model"}
}

// ModelFile is the file name a Whisper model is stored under
// ("base.en" → "ggml-base.en.bin"), the same as upstream.
func ModelFile(name string) string { return "ggml-" + name + ".bin" }

// EnglishOnly reports whether a model only understands English.
func EnglishOnly(model string) bool { return strings.HasSuffix(model, ".en") }

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
