package video

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeWhisper is a stand-in whisper-server: /health, /inference, /load.
type fakeWhisper struct {
	mu       sync.Mutex
	ready    bool
	loaded   string
	gotLang  string
	gotBytes int
}

func (f *fakeWhisper) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"loading model"}`))
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /inference", func(w http.ResponseWriter, r *http.Request) {
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"no file"}`))
			return
		}
		b := make([]byte, 1<<20)
		n, _ := file.Read(b)
		f.mu.Lock()
		f.gotLang, f.gotBytes = r.FormValue("language"), n
		f.mu.Unlock()
		w.Write([]byte(`{"text":" Grate the  zucchini.\n"}`))
	})
	mux.HandleFunc("POST /load", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.loaded = r.FormValue("model")
		f.mu.Unlock()
		w.Write([]byte("Load was successful!"))
	})
	return mux
}

func newFakeWhisper(t *testing.T, ready bool) (*fakeWhisper, string) {
	f := &fakeWhisper{ready: ready}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func withModel(t *testing.T, names ...string) string {
	dir := t.TempDir()
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, ModelFile(n)), []byte("x"), 0o644)
	}
	return dir
}

func TestTranscribeViaServer(t *testing.T) {
	f, url := newFakeWhisper(t, true)
	tools := Tools{ServerURL: url, ModelsPath: withModel(t, "base.en")}
	wav := filepath.Join(t.TempDir(), "a.wav")
	os.WriteFile(wav, silentWAV(1), 0o644)

	text, model, err := Transcribe(context.Background(), tools, wav)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Grate the zucchini." || model != "base.en" {
		t.Errorf("text %q model %q", text, model)
	}
	if f.gotLang != "en" || f.gotBytes == 0 {
		t.Errorf("server got language %q, %d bytes", f.gotLang, f.gotBytes)
	}
}

func TestCapabilityWithServer(t *testing.T) {
	core := t.TempDir()
	for _, sub := range []string{"yt-dlp", "ffmpeg"} {
		name := map[string]string{"yt-dlp": exeName(ToolYtDlp), "ffmpeg": exeName(ToolFFmpeg)}[sub]
		os.MkdirAll(filepath.Join(core, sub), 0o755)
		os.WriteFile(filepath.Join(core, sub, name), []byte("x"), 0o755)
	}

	// Server down (the sidecar waiting for a model) and no model yet: both
	// missing, so "Download now" fetches a model.
	f, url := newFakeWhisper(t, false)
	tools := Tools{Dir: core, ServerURL: url, ModelsPath: t.TempDir()}
	if level, missing := tools.Capability(); level != CapCaption || strings.Join(missing, ",") != "whisper,model" {
		t.Errorf("server down: %s %v", level, missing)
	}

	// Server up: full, no local whisper-cli needed.
	f.mu.Lock()
	f.ready = true
	f.mu.Unlock()
	forgetServerHealth(url)
	if level, missing := tools.Capability(); level != CapFull {
		t.Errorf("server up: %s %v", level, missing)
	}
}

func TestUseModelLoadsItOnTheServer(t *testing.T) {
	f, url := newFakeWhisper(t, true)
	models := withModel(t, "base.en", "small.en")
	tools := Tools{ServerURL: url, ModelsPath: models}
	if err := tools.UseModel(context.Background(), "small.en"); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(models, ModelFile("small.en")); f.loaded != want {
		t.Errorf("server loaded %q, want %q", f.loaded, want)
	}
	if tools.ActiveModel() != "small.en" {
		t.Errorf("active = %q", tools.ActiveModel())
	}
}

func TestStartSkipsWhisperWithServer(t *testing.T) {
	_, url := newFakeWhisper(t, true)
	in := NewInstaller(Tools{Dir: t.TempDir()})
	in.ServerURL = func() string { return url }
	if err := in.Start([]string{CompWhisper}, DefaultModel); err == nil || !strings.Contains(err.Error(), "nothing to install") {
		t.Fatalf("whisper-cli must not be installed when a server transcribes: %v", err)
	}
}

func TestVerifyWithServer(t *testing.T) {
	_, url := newFakeWhisper(t, true)
	in := NewInstaller(Tools{Dir: t.TempDir(), ModelsPath: withModel(t, "base.en")})
	in.ServerURL = func() string { return url }
	checks := in.Verify(context.Background())
	if !checks[CompWhisper].OK || !strings.Contains(checks[CompWhisper].Detail, url) {
		t.Errorf("whisper check = %+v", checks[CompWhisper])
	}
	if !checks[CompModel].OK || !strings.Contains(checks[CompModel].Detail, "base.en") {
		t.Errorf("model check = %+v", checks[CompModel])
	}
}

func TestServerBaseRejectsJunk(t *testing.T) {
	for _, bad := range []string{"whisper:8081", "file:///etc/passwd", "ftp://x", ""} {
		if _, err := serverBase(bad); err == nil {
			t.Errorf("serverBase(%q) accepted", bad)
		}
	}
	if got, _ := serverBase(" http://whisper:8081/ "); got != "http://whisper:8081" {
		t.Errorf("normalized = %q", got)
	}
}

func TestUseModelSkipsLoadWithoutSharedFolder(t *testing.T) {
	f, url := newFakeWhisper(t, true)
	dir := t.TempDir()
	tools := Tools{Dir: dir, ServerURL: url} // models in TOOLS_DIR, not shared
	os.MkdirAll(tools.ModelsDir(), 0o755)
	os.WriteFile(filepath.Join(tools.ModelsDir(), ModelFile("base.en")), []byte("x"), 0o644)
	if err := tools.UseModel(context.Background(), "base.en"); err != nil {
		t.Fatal(err)
	}
	if f.loaded != "" {
		t.Errorf("sent /load %q to a server that can't see this path", f.loaded)
	}
}
