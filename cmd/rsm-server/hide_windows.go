//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps console helpers (tailscale.exe, netsh.exe) from flashing
// a window when the setup page or the installer runs them.
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
