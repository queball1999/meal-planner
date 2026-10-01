package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"goeat/config"
)

// installFixture is a desktop server with a newer release out, a fake
// bundled updater and its stamped hash, and process start recorded instead
// of run.
type installFixture struct {
	*rbacFixture
	started []*exec.Cmd
	exe     string
}

func newInstallFixture(t *testing.T, kind string) *installFixture {
	t.Helper()
	dir := t.TempDir()
	updater := filepath.Join(dir, "updater.exe")
	body := []byte("pretend updater")
	if err := os.WriteFile(updater, body, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	exe := filepath.Join(dir, "Go Eat.exe")

	f := &installFixture{exe: exe}
	f.rbacFixture = newRBACFixtureWith(t, func(c *config.Config) {
		c.UpdateCheck = true
		c.Desktop = true
		c.DesktopInstall = config.DesktopInstall{
			Updater: updater, Kind: kind, ShellPID: 4242, ShellExe: exe, DataDir: dir,
		}
	})
	withRelease(t, f.rbacFixture, "v0.0.2", "v0.0.3")
	f.srv.SetBundledUpdater(hex.EncodeToString(sum[:]))
	f.srv.startProcess = func(cmd *exec.Cmd) error {
		f.started = append(f.started, cmd)
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(cmd.Path)) })
		return nil
	}
	return f
}

func (f *installFixture) install(t *testing.T) *http.Response {
	t.Helper()
	return f.do("root", "POST", "/about/update", nil).Result()
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// TestInstallStartsVerifiedCopy: Install runs a temp copy of the bundled
// updater, never the original, with what only Go Eat knows on its command line.
func TestInstallStartsVerifiedCopy(t *testing.T) {
	f := newInstallFixture(t, config.InstallKindInstaller)

	resp := f.install(t)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/about#updates" {
		t.Fatalf("POST /about/update: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if len(f.started) != 1 {
		t.Fatalf("started %d updaters, want 1", len(f.started))
	}
	cmd := f.started[0]

	if cmd.Path == f.srv.cfg.DesktopInstall.Updater || !strings.Contains(filepath.Dir(cmd.Path), updaterTempPrefix) {
		t.Errorf("ran %s, want a copy in a %s* temp folder", cmd.Path, updaterTempPrefix)
	}
	if got, _ := os.ReadFile(cmd.Path); string(got) != "pretend updater" {
		t.Errorf("copy holds %q", got)
	}

	args := cmd.Args[1:]
	for flag, want := range map[string]string{
		"--calling-pid":     "4242",
		"--executable":      f.exe,
		"--current-version": "test",
		"--log-file":        filepath.Join(filepath.Dir(f.exe), "logs"),
	} {
		if got := argAfter(args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if !slices.Contains(args, "--gui") || slices.Contains(args, "--asset-pattern") {
		t.Errorf("installer args: %q", args)
	}
}

func TestInstallPortablePicksZipAndFolder(t *testing.T) {
	f := newInstallFixture(t, config.InstallKindPortable)
	f.install(t)

	if len(f.started) != 1 {
		t.Fatalf("started %d updaters", len(f.started))
	}
	args := f.started[0].Args[1:]
	if got := argAfter(args, "--asset-pattern"); got != portableAssetPattern {
		t.Errorf("--asset-pattern = %q", got)
	}
	if got := argAfter(args, "--target-dir"); got != filepath.Dir(f.exe) {
		t.Errorf("--target-dir = %q", got)
	}
}

// TestInstallRefusesTamperedUpdater: an updater that isn't the one CI built
// is never started, and its copy is cleaned up.
func TestInstallRefusesTamperedUpdater(t *testing.T) {
	f := newInstallFixture(t, config.InstallKindInstaller)
	if err := os.WriteFile(f.srv.cfg.DesktopInstall.Updater, []byte("swapped"), 0o700); err != nil {
		t.Fatal(err)
	}

	before, _ := filepath.Glob(filepath.Join(os.TempDir(), updaterTempPrefix+"*"))
	f.install(t)
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), updaterTempPrefix+"*"))

	if len(f.started) != 0 {
		t.Fatal("a tampered updater was started")
	}
	if len(after) > len(before) {
		t.Error("the rejected copy was left in the temp folder")
	}
}

func TestInstallIgnoresDoubleClick(t *testing.T) {
	f := newInstallFixture(t, config.InstallKindInstaller)
	f.install(t)
	f.install(t)

	if len(f.started) != 1 {
		t.Fatalf("started %d updaters, want 1", len(f.started))
	}
}

func TestInstallBlocked(t *testing.T) {
	cases := map[string]func(f *installFixture){
		"dev build":         func(f *installFixture) { f.srv.cfg.DesktopInstall.Kind = config.InstallKindDev },
		"unknown install":   func(f *installFixture) { f.srv.cfg.DesktopInstall.Kind = "" },
		"no updater":        func(f *installFixture) { f.srv.SetBundledUpdater("") },
		"no shell":          func(f *installFixture) { f.srv.cfg.DesktopInstall.ShellPID = 0 },
		"already on latest": func(f *installFixture) { withRelease(t, f.rbacFixture, "v0.0.3", "v0.0.3") },
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t, config.InstallKindInstaller)
			block(f)

			f.install(t)
			if len(f.started) != 0 {
				t.Fatal("updater started")
			}
			body := f.do("root", "GET", "/about", nil).Body.String()
			if strings.Contains(body, `action="/about/update"`) {
				t.Error("About still offers Install")
			}
		})
	}
}

func TestInstallIsAdminAndDesktopOnly(t *testing.T) {
	f := newInstallFixture(t, config.InstallKindInstaller)
	for _, user := range []string{"olga", "alice", "vera"} {
		if w := f.do(user, "POST", "/about/update", nil); w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want refused", user, w.Code)
		}
	}
	if len(f.started) != 0 {
		t.Fatal("a non-admin started the updater")
	}

	f.srv.cfg.Desktop = false
	if w := f.do("root", "POST", "/about/update", nil); w.Code != http.StatusNotFound {
		t.Errorf("server: %d, want 404", w.Code)
	}
}

func TestAboutOffersInstall(t *testing.T) {
	f := newInstallFixture(t, config.InstallKindInstaller)

	body := f.do("root", "GET", "/about", nil).Body.String()
	for _, want := range []string{`action="/about/update"`, "Install v0.0.3", `data-confirm-title="Install v0.0.3"`} {
		if !strings.Contains(body, want) {
			t.Errorf("About missing %q", want)
		}
	}
}
