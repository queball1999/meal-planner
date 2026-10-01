package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goeat/config"
	"goeat/middleware"
)

// ── Installing an update on desktop (phase 15, commit 3) ───────────────────
//
// About -> Updates -> Install starts the branded QUpdateTool updater that CI
// bundled beside the app. The updater stops the desktop shell (and with it
// this sidecar), downloads the release, verifies it against the pinned
// signing key, installs it and relaunches Go Eat. Servers never get here:
// they're updated by hand.

const (
	// portableAssetPattern picks the portable zip over the installer for a
	// portable install; the brand's own preference would pick the installer.
	portableAssetPattern = `windows-x64-portable\.zip$`

	// updaterRelaunchGuard stops a double click starting two updaters: the
	// shell is usually gone within seconds, but a failed start shouldn't
	// lock Install out for the rest of the session.
	updaterRelaunchGuard = 2 * time.Minute

	// updaterTempPrefix names the folder each launch copies the updater into.
	updaterTempPrefix = "goeat-updater-"
)

// installBlocker says why Install can't be offered, or "" when it can. The
// About page shows the reason in place of the button.
func (s *Server) installBlocker() string {
	d := s.cfg.DesktopInstall
	st := s.updates.Status()
	switch {
	case !s.cfg.Desktop:
		return "Servers are updated by hand."
	case d.Kind == config.InstallKindDev:
		return "This is a development build run from source; it doesn't update itself."
	case !d.SelfUpdating():
		return "Go Eat couldn't tell how it was installed, so it can't update itself."
	case s.updaterSHA256 == "" || d.Updater == "":
		return "This build doesn't include the updater."
	case d.ShellPID == 0 || d.ShellExe == "":
		return "The app window didn't say where it's running from, so it can't be restarted after the update."
	case !st.Available || st.Latest == nil:
		return "There's no newer version to install."
	}
	return ""
}

// updaterArgs is the updater's command line for this install. Only Go Eat
// knows these; the brand (desktop/updater-brand.yaml) supplies the rest.
func (s *Server) updaterArgs() []string {
	d := s.cfg.DesktopInstall
	args := []string{
		"--gui",
		"--calling-pid", strconv.Itoa(d.ShellPID),
		"--executable", d.ShellExe,
		"--current-version", s.version,
	}
	if d.DataDir != "" {
		// Beside goeat.log, so a support bundle has both.
		args = append(args, "--log-file", filepath.Join(d.DataDir, "logs"))
	}
	if d.Kind == config.InstallKindPortable {
		args = append(args,
			"--asset-pattern", portableAssetPattern,
			"--target-dir", filepath.Dir(d.ShellExe))
	}
	return args
}

// handleUpdateInstall is About -> Updates -> Install.
func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Desktop {
		http.NotFound(w, r)
		return
	}
	if why := s.installBlocker(); why != "" {
		s.setNotify(w, NotifyWarning, why)
		http.Redirect(w, r, "/about#updates", http.StatusSeeOther)
		return
	}

	s.updaterMu.Lock()
	recent := !s.updaterStartedAt.IsZero() && time.Since(s.updaterStartedAt) < updaterRelaunchGuard
	if !recent {
		s.updaterStartedAt = time.Now()
	}
	s.updaterMu.Unlock()
	if recent {
		s.setNotify(w, NotifyInfo, "The updater is already starting.")
		http.Redirect(w, r, "/about#updates", http.StatusSeeOther)
		return
	}

	latest := s.updates.Status().Latest.Version
	var actorID *int64
	if u := middleware.UserFromCtx(r); u != nil {
		actorID = &u.ID
	}

	if err := s.launchUpdater(); err != nil {
		s.updaterMu.Lock()
		s.updaterStartedAt = time.Time{}
		s.updaterMu.Unlock()
		log.Printf("update install: %v", err)
		s.logEvent(r, actorID, "update.install.failed", "release", latest, fmt.Sprintf(`{"error":%q}`, err.Error()))
		s.setNotify(w, NotifyDanger, "Couldn't start the updater: "+err.Error())
		http.Redirect(w, r, "/about#updates", http.StatusSeeOther)
		return
	}

	log.Printf("update install: started the updater for %s (running %s)", latest, s.version)
	s.logEvent(r, actorID, "update.install.started", "release", latest,
		fmt.Sprintf(`{"from":%q,"kind":%q}`, s.version, s.cfg.DesktopInstall.Kind))
	s.setNotify(w, NotifySuccess,
		"Installing "+latest+". Go Eat will close, update, and open again by itself.")
	http.Redirect(w, r, "/about#updates", http.StatusSeeOther)
}

// errUpdaterTampered means the bundled updater isn't the one CI built.
var errUpdaterTampered = errors.New("the updater doesn't match this build; reinstall Go Eat from the release page")

// launchUpdater copies the bundled updater to a fresh temp folder, checks
// the copy against the hash stamped at build time, and starts it detached.
//
// The copy is what gets checked and run, so the file can't be swapped
// between the check and the launch. Running from outside the install also
// lets a portable update replace the bundled updater.exe (Windows won't
// overwrite a running exe), and gets an AppImage's updater out of the
// read-only mount that the update replaces.
func (s *Server) launchUpdater() error {
	removeStaleUpdaterCopies()

	dir, err := os.MkdirTemp("", updaterTempPrefix)
	if err != nil {
		return fmt.Errorf("make a temp folder: %w", err)
	}
	copyPath := filepath.Join(dir, filepath.Base(s.cfg.DesktopInstall.Updater))

	sum, err := copyAndHash(s.cfg.DesktopInstall.Updater, copyPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("copy the updater: %w", err)
	}
	if !strings.EqualFold(sum, s.updaterSHA256) {
		_ = os.RemoveAll(dir)
		log.Printf("update install: updater sha256 %s, build expects %s", sum, s.updaterSHA256)
		return errUpdaterTampered
	}

	cmd := exec.Command(copyPath, s.updaterArgs()...)
	cmd.Dir = dir
	detach(cmd)
	if err := s.startProcess(cmd); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	return nil
}

// copyAndHash copies src to dst (executable, owner only) and returns the hex
// SHA-256 of what was written.
func copyAndHash(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// removeStaleUpdaterCopies deletes the temp copies earlier launches left
// behind. A copy can't delete itself while it runs, so each launch tidies
// up after the last; one still in use (Windows) is simply skipped.
func removeStaleUpdaterCopies() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), updaterTempPrefix+"*"))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() && time.Since(info.ModTime()) > time.Hour {
			_ = os.RemoveAll(m)
		}
	}
}

// startDetached is the default Server.startProcess.
func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the updater: %w", err)
	}
	// Not waited on: the updater outlives this process by design.
	return cmd.Process.Release()
}
