package video

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow stops each tool opening a console window: the desktop app's
// sidecar has no console of its own, so Windows would give every yt-dlp,
// ffmpeg and whisper run a fresh one that flashes up over the app.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}
