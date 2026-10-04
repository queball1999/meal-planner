package video

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func serve(t *testing.T, body []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	body := []byte("the real file")
	srv := serve(t, body)
	dst := filepath.Join(t.TempDir(), "f")

	if err := download(context.Background(), srv.Client(), asset{URL: srv.URL, SHA256: sum(body), Size: int64(len(body))}, dst, nil); err != nil {
		t.Fatalf("good download: %v", err)
	}
	err := download(context.Background(), srv.Client(), asset{URL: srv.URL, SHA256: sum([]byte("other")), Size: int64(len(body))}, dst, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered download: err = %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Error("a file that failed its checksum must be deleted")
	}
}

func TestDownloadRefusesOversizedBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 3<<20)
	srv := serve(t, body)
	err := download(context.Background(), srv.Client(), asset{URL: srv.URL, SHA256: sum(body), Size: 10}, filepath.Join(t.TempDir(), "f"), nil)
	if err == nil || !strings.Contains(err.Error(), "larger than expected") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnpackZipFlattensAndFilters(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"Release/whisper-cli.exe", "Release/whisper.dll", "Release/bench.exe", "../../evil.dll"} {
		w, _ := zw.Create(name)
		w.Write([]byte(name))
	}
	zw.Close()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "w.zip")
	os.WriteFile(src, buf.Bytes(), 0o644)
	out := filepath.Join(tmp, "out")
	os.Mkdir(out, 0o755)

	if err := unpack(asset{Format: "zip"}, src, out, "whisper-cli.exe"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "evil.dll,whisper-cli.exe,whisper.dll" {
		t.Errorf("unpacked %q (want flattened, bench.exe dropped)", names)
	}
	if _, err := os.Stat(filepath.Join(tmp, "evil.dll")); !os.IsNotExist(err) {
		t.Error("a ../ entry escaped the target folder")
	}
}

func TestUnpackTgzKeepsSiblingSymlinksOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; the tgz path is Linux-only")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		tw.WriteHeader(h)
		tw.Write([]byte(body))
	}
	add(&tar.Header{Name: "whisper-bin/whisper-cli", Typeflag: tar.TypeReg, Mode: 0o755}, "bin")
	add(&tar.Header{Name: "whisper-bin/libwhisper.so.1.9.4", Typeflag: tar.TypeReg, Mode: 0o755}, "lib")
	add(&tar.Header{Name: "whisper-bin/libwhisper.so.1", Typeflag: tar.TypeSymlink, Linkname: "libwhisper.so.1.9.4"}, "")
	add(&tar.Header{Name: "whisper-bin/libevil.so", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	tw.Close()
	gz.Close()

	tmp := t.TempDir()
	src := filepath.Join(tmp, "w.tgz")
	os.WriteFile(src, buf.Bytes(), 0o644)
	out := filepath.Join(tmp, "out")
	os.Mkdir(out, 0o755)
	if err := unpack(asset{Format: "tgz"}, src, out, "whisper-cli"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(out, "libwhisper.so.1")); err != nil || target != "libwhisper.so.1.9.4" {
		t.Errorf("sibling symlink: %q, %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(out, "libevil.so")); !os.IsNotExist(err) {
		t.Error("a symlink pointing outside the folder was kept")
	}
}

func TestLatestYtDlpUsesGitHubDigest(t *testing.T) {
	name, ok := ytDlpAssetNames[platformKey()]
	if !ok || isMusl() {
		t.Skip("no yt-dlp asset for this platform")
	}
	digest := strings.Repeat("ab", 32)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"2026.08.19","assets":[
			{"name":"%s","size":123,"digest":"sha256:%s","browser_download_url":"https://github.com/yt-dlp/yt-dlp/releases/download/2026.08.19/%s"},
			{"name":"other","size":1,"digest":"","browser_download_url":"https://example.com/x"}]}`, name, digest, name)
	}))
	defer api.Close()
	old := githubAPI
	githubAPI = api.URL
	defer func() { githubAPI = old }()

	a, err := latestYtDlp(context.Background(), api.Client())
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != digest || a.Version != "2026.08.19" || a.Size != 123 {
		t.Errorf("asset = %+v", a)
	}
}

func TestCapabilityLevels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "") // only TOOLS_DIR counts
	tools := Tools{Dir: dir}
	put := func(sub, name string) {
		p := filepath.Join(dir, sub, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o755)
	}
	if level, missing := tools.Capability(); level != CapNone || len(missing) != 4 {
		t.Errorf("empty: %s %v", level, missing)
	}
	put("yt-dlp", exeName(ToolYtDlp))
	put("ffmpeg", exeName(ToolFFmpeg))
	if level, missing := tools.Capability(); level != CapCaption || strings.Join(missing, ",") != "whisper,model" {
		t.Errorf("no whisper: %s %v", level, missing)
	}
	put("whisper", exeName(ToolWhisper))
	put("models", ModelFile("base.en"))
	if level, _ := tools.Capability(); level != CapFull {
		t.Errorf("everything: %s", level)
	}
}

func TestActiveModelPicksTranscriptionModel(t *testing.T) {
	dir := t.TempDir()
	tools := Tools{Dir: dir}
	os.MkdirAll(tools.ModelsDir(), 0o755)
	for _, n := range []string{"base.en", "small"} {
		os.WriteFile(filepath.Join(tools.ModelsDir(), ModelFile(n)), []byte("x"), 0o644)
	}
	if err := tools.SetActiveModel("small"); err != nil {
		t.Fatal(err)
	}
	if _, name, _ := tools.ModelPath(); name != "small" {
		t.Errorf("active model ignored: %s", name)
	}
	if err := tools.SetActiveModel("tiny"); err == nil {
		t.Error("activating a model that isn't installed should fail")
	}
	if err := tools.RemoveModel("small"); err != nil {
		t.Fatal(err)
	}
	if _, name, _ := tools.ModelPath(); name != "base.en" {
		t.Errorf("after removing the active model: %s", name)
	}
}

func TestSilentWAVHeader(t *testing.T) {
	b := silentWAV(1)
	if len(b) != 44+32000 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Errorf("bad WAV: len %d", len(b))
	}
}

// TestVerifyLive runs Verify against the real tools:
//
//	GOEAT_TOOLS_DIR=../data/tools go test ./video -run VerifyLive -v
func TestVerifyLive(t *testing.T) {
	dir := os.Getenv("GOEAT_TOOLS_DIR")
	if dir == "" {
		t.Skip("set GOEAT_TOOLS_DIR to run")
	}
	in := NewInstaller(Tools{Dir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for c, check := range in.Verify(ctx) {
		t.Logf("%-8s ok=%v %s", c, check.OK, check.Detail)
	}
	s := in.Snapshot()
	t.Logf("capability=%s missing=%v", s.Capability, s.Missing)
}

// TestInstallLive downloads every component for real into a temp folder,
// then verifies it (about 130 MB with the tiny model):
//
//	GOEAT_INSTALL_IT=1 go test ./video -run InstallLive -v -timeout 20m
func TestInstallLive(t *testing.T) {
	if os.Getenv("GOEAT_INSTALL_IT") == "" {
		t.Skip("set GOEAT_INSTALL_IT=1 to run")
	}
	t.Setenv("PATH", "") // prove the downloaded copies are the ones used
	in := NewInstaller(Tools{Dir: t.TempDir()})
	if err := in.Start(Components, "tiny.en"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Minute)
	for in.Snapshot().Running {
		if time.Now().After(deadline) {
			t.Fatal("install took over 15 minutes")
		}
		time.Sleep(time.Second)
	}
	s := in.Snapshot()
	for _, ts := range s.Tools {
		t.Logf("%-8s installed=%v source=%s version=%q progress=%+v check=%+v", ts.ID, ts.Installed, ts.Source, ts.Version, *ts.Progress, *ts.Check)
		if !ts.Check.OK {
			t.Errorf("%s failed its check", ts.ID)
		}
	}
	if s.Capability != CapFull {
		t.Errorf("capability = %s", s.Capability)
	}
}
