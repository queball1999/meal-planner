//go:build !windows

package video

import "os/exec"

// hideWindow is a no-op off Windows: there are no console windows to hide.
func hideWindow(*exec.Cmd) {}
