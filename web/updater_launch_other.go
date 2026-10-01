//go:build !windows

package web

import (
	"os/exec"
	"syscall"
)

// detach starts the updater in its own session, so it keeps running when
// the updater itself stops the desktop shell and this sidecar exits, and a
// signal to this process group doesn't reach it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
