package config

import (
	"os"
	"strconv"
	"strings"
)

// Install kinds the desktop shell reports in GOEAT_INSTALL_KIND.
const (
	InstallKindInstaller = "installer" // Windows NSIS installer
	InstallKindPortable  = "portable"  // Windows portable zip (portable.txt beside the exe)
	InstallKindDeb       = "deb"       // Linux .deb
	InstallKindAppImage  = "appimage"  // Linux AppImage
	InstallKindDev       = "dev"       // a debug build run from source: never self-updates
)

// DesktopInstall is what the Tauri shell tells its sidecar about the running
// install, so About -> Updates can start the bundled updater (phase 15).
// Only the shell knows these: where its resources are, how it was installed,
// and its own process, which the updater stops and relaunches.
type DesktopInstall struct {
	Updater  string // GOEAT_UPDATER: the bundled updater; "" if none was found
	Kind     string // GOEAT_INSTALL_KIND: one of the InstallKind* values
	ShellPID int    // GOEAT_SHELL_PID: the shell process
	// ShellExe (GOEAT_SHELL_EXE) is what the updater relaunches, and for a
	// portable zip the folder it unpacks into. For an AppImage it's the
	// .AppImage file, not the binary inside the mount.
	ShellExe string
	DataDir  string // GOEAT_DATA_DIR: where the updater writes its log
}

// SelfUpdating reports whether this install kind can be updated in place.
func (d DesktopInstall) SelfUpdating() bool {
	switch d.Kind {
	case InstallKindInstaller, InstallKindPortable, InstallKindDeb, InstallKindAppImage:
		return true
	}
	return false
}

func loadDesktopInstall() DesktopInstall {
	pid, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("GOEAT_SHELL_PID")))
	return DesktopInstall{
		Updater:  strings.TrimSpace(os.Getenv("GOEAT_UPDATER")),
		Kind:     strings.TrimSpace(os.Getenv("GOEAT_INSTALL_KIND")),
		ShellPID: pid,
		ShellExe: strings.TrimSpace(os.Getenv("GOEAT_SHELL_EXE")),
		DataDir:  strings.TrimSpace(os.Getenv("GOEAT_DATA_DIR")),
	}
}
