package video

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Capability levels for video import, from what is installed.
const (
	// CapNone: yt-dlp or ffmpeg is missing, so a video can't be read at all.
	CapNone = "none"
	// CapCaption: the post can be read, but there's no speech-to-text, so
	// only the caption and comments reach the model.
	CapCaption = "caption"
	// CapFull: caption, comments and the spoken audio.
	CapFull = "full"
)

// Capability reports how much a video import can do with the tools present,
// and which components are missing. It only checks that files exist - cheap
// enough for every page load; Verify actually runs them.
func (t Tools) Capability() (level string, missing []string) {
	if _, err := t.Find(ToolYtDlp); err != nil {
		missing = append(missing, CompYtDlp)
	}
	if _, err := t.Find(ToolFFmpeg); err != nil {
		missing = append(missing, CompFFmpeg)
	}
	core := len(missing) == 0
	if _, err := t.Find(ToolWhisper); err != nil {
		missing = append(missing, CompWhisper)
	}
	if _, _, err := t.ModelPath(); err != nil {
		missing = append(missing, CompModel)
	}
	switch {
	case !core:
		return CapNone, missing
	case len(missing) > 0:
		return CapCaption, missing
	}
	return CapFull, nil
}

// Progress is one component's install state.
type Progress struct {
	State   string `json:"state"` // queued | downloading | installing | done | failed
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"`
}

// Check is the result of actually running one tool.
type Check struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Installer downloads components in the background and remembers how the
// last install and the last check went, for the Settings page to poll.
type Installer struct {
	Tools  Tools
	Client *http.Client

	mu        sync.Mutex
	running   bool
	progress  map[string]*Progress
	checks    map[string]Check
	checkedAt time.Time
}

// NewInstaller returns an Installer for t.
func NewInstaller(t Tools) *Installer {
	return &Installer{
		Tools: t,
		Client: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
		}},
		progress: map[string]*Progress{},
		checks:   map[string]Check{},
	}
}

// ErrInstallRunning is returned by Start while another install is going.
var ErrInstallRunning = errors.New("an install is already running")

// Start installs components (any of Components; model names the Whisper
// model for CompModel) one after another in the background, then runs
// Verify. It returns at once.
func (in *Installer) Start(components []string, model string) error {
	if in.Tools.Dir == "" {
		return errors.New("TOOLS_DIR is not set, so there is nowhere to install to")
	}
	want := map[string]bool{}
	for _, c := range components {
		want[c] = true
	}
	var order []string
	for _, c := range Components {
		if want[c] {
			order = append(order, c)
		}
	}
	if len(order) == 0 {
		return errors.New("nothing to install")
	}
	if want[CompModel] {
		if _, ok := modelInfo(model); !ok {
			return fmt.Errorf("unknown model %q", model)
		}
	}

	in.mu.Lock()
	if in.running {
		in.mu.Unlock()
		return ErrInstallRunning
	}
	in.running = true
	for _, c := range order {
		in.progress[c] = &Progress{State: "queued", Total: DownloadSize(c, model)}
	}
	in.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		for _, c := range order {
			version, err := in.Tools.install(ctx, in.Client, c, model, func(state string, done, total int64) {
				in.set(c, func(p *Progress) {
					p.State, p.Done = state, done
					if total > 0 {
						p.Total = total
					}
				})
			})
			in.set(c, func(p *Progress) {
				if err != nil {
					p.State, p.Error = "failed", err.Error()
					return
				}
				p.State, p.Version, p.Done = "done", version, p.Total
			})
		}
		in.Verify(ctx)
		in.mu.Lock()
		in.running = false
		in.mu.Unlock()
	}()
	return nil
}

func (in *Installer) set(c string, f func(*Progress)) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if p := in.progress[c]; p != nil {
		f(p)
	}
}

// Verify runs every tool to prove it works on this machine - a binary that
// is present but can't start (wrong libc, a missing DLL, blocked by
// antivirus) only shows up this way - and remembers the results.
func (in *Installer) Verify(ctx context.Context) map[string]Check {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	checks := in.Tools.verify(ctx)
	in.mu.Lock()
	in.checks, in.checkedAt = checks, time.Now()
	in.mu.Unlock()
	return checks
}

func (t Tools) verify(ctx context.Context) map[string]Check {
	checks := map[string]Check{}

	versionCheck := func(tool string, args []string, parse func(string) string) Check {
		path, err := t.Find(tool)
		if err != nil {
			return Check{Detail: "Not installed"}
		}
		out, err := command(ctx, path, args...).CombinedOutput()
		if err != nil {
			return Check{Detail: "Won't run: " + firstLine(lastErrorLine(string(out)), err.Error())}
		}
		return Check{OK: true, Detail: parse(string(out))}
	}
	checks[CompYtDlp] = versionCheck(ToolYtDlp, []string{"--version"}, func(s string) string {
		return "yt-dlp " + strings.TrimSpace(s)
	})
	checks[CompFFmpeg] = versionCheck(ToolFFmpeg, []string{"-version"}, func(s string) string {
		line, _, _ := strings.Cut(firstLine(s, "ffmpeg"), " Copyright")
		return line
	})
	checks[CompWhisper] = versionCheck(ToolWhisper, []string{"--version"}, func(s string) string {
		for _, l := range strings.Split(s, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "whisper.cpp version:"); ok {
				return "whisper.cpp " + strings.TrimSpace(v)
			}
		}
		return "whisper.cpp"
	})

	// The model check transcribes a second of silence: it proves whisper
	// can load the model, not just that both files exist.
	switch _, model, err := t.ModelPath(); {
	case err != nil:
		checks[CompModel] = Check{Detail: "Not installed"}
	case !checks[CompWhisper].OK:
		checks[CompModel] = Check{Detail: model + " - can't test until whisper.cpp runs"}
	default:
		dir, err := os.MkdirTemp("", "goeat-verify-*")
		if err != nil {
			checks[CompModel] = Check{Detail: err.Error()}
			break
		}
		defer os.RemoveAll(dir)
		wav := filepath.Join(dir, "silence.wav")
		start := time.Now()
		if err := os.WriteFile(wav, silentWAV(1), 0o644); err != nil {
			checks[CompModel] = Check{Detail: err.Error()}
		} else if _, _, err := Transcribe(ctx, t, wav); err != nil {
			checks[CompModel] = Check{Detail: model + " failed to load: " + err.Error()}
		} else {
			checks[CompModel] = Check{OK: true, Detail: fmt.Sprintf("%s loaded and ran in %.1fs", model, time.Since(start).Seconds())}
		}
	}
	return checks
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if s == "" {
		return fallback
	}
	return s
}

// silentWAV is seconds of 16 kHz mono 16-bit silence.
func silentWAV(seconds int) []byte {
	const rate = 16000
	data := rate * 2 * seconds
	b := make([]byte, 44+data)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	return b
}

// ToolState is one component as the Settings page shows it.
type ToolState struct {
	ID         string    `json:"id"`
	Installed  bool      `json:"installed"`
	Source     string    `json:"source"` // "downloaded" | "system" | ""
	Path       string    `json:"path"`
	Version    string    `json:"version"`
	CanInstall bool      `json:"can_install"`
	Size       int64     `json:"size"`
	Progress   *Progress `json:"progress,omitempty"`
	Check      *Check    `json:"check,omitempty"`
}

// ModelState is one offered Whisper model.
type ModelState struct {
	ModelInfo
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
}

// Snapshot is everything the Settings page and the import banner show.
type Snapshot struct {
	Capability string       `json:"capability"`
	Missing    []string     `json:"missing"`
	Running    bool         `json:"running"`
	Tools      []ToolState  `json:"tools"`
	Models     []ModelState `json:"models"`
	CheckedAt  string       `json:"checked_at,omitempty"`
	ToolsDir   string       `json:"tools_dir"`
}

// Snapshot reports the current state of every component.
func (in *Installer) Snapshot() Snapshot {
	t := in.Tools
	level, missing := t.Capability()
	s := Snapshot{Capability: level, Missing: missing, ToolsDir: t.Dir}

	_, activeModel, modelErr := t.ModelPath()
	installedModels := map[string]bool{}
	for _, m := range t.InstalledModels() {
		installedModels[m] = true
	}
	for _, m := range Models {
		s.Models = append(s.Models, ModelState{ModelInfo: m, Installed: installedModels[m.Name], Active: modelErr == nil && m.Name == activeModel})
	}

	tools := map[string]string{CompYtDlp: ToolYtDlp, CompFFmpeg: ToolFFmpeg, CompWhisper: ToolWhisper}
	for _, c := range Components {
		ts := ToolState{ID: c, CanInstall: CanInstall(c) && t.Dir != "", Size: DownloadSize(c, DefaultModel)}
		if c == CompModel {
			if modelErr == nil {
				ts.Installed, ts.Source, ts.Version = true, "downloaded", activeModel
			}
		} else if path, err := t.Find(tools[c]); err == nil {
			ts.Installed, ts.Path = true, path
			ts.Source = "system"
			if t.Dir != "" && strings.HasPrefix(filepath.Clean(path), filepath.Clean(t.Dir)+string(filepath.Separator)) {
				ts.Source = "downloaded"
				ts.Version = t.InstalledVersion(c)
			}
		}
		s.Tools = append(s.Tools, ts)
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	s.Running = in.running
	for i := range s.Tools {
		if p, ok := in.progress[s.Tools[i].ID]; ok {
			cp := *p
			s.Tools[i].Progress = &cp
		}
		if c, ok := in.checks[s.Tools[i].ID]; ok {
			cc := c
			s.Tools[i].Check = &cc
		}
	}
	if !in.checkedAt.IsZero() {
		s.CheckedAt = in.checkedAt.Format(time.RFC3339)
	}
	return s
}
