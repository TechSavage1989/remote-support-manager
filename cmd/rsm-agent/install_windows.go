//go:build windows

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
	"github.com/TechSavage1989/remote-support-manager/internal/update"
	"github.com/TechSavage1989/remote-support-manager/internal/updateui"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const (
	exeName     = "RemoteSupportAgent.exe"
	runName     = "RemoteSupportManagerAgent"
	uninstallID = "RemoteSupportManagerAgent"
)

// install copies the running executable to the per-user Programs folder
// (no administrator rights), adds a visible sign-in entry and an entry in
// Settings > Apps.
func install(autostart bool) (string, error) {
	dir := winapi.ProgramsDir("Remote Support Manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, exeName)
	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
	}
	if autostart {
		if err := winapi.SetRunAtSignIn(runName, `"`+dst+`"`, true); err != nil {
			return "", err
		}
	}
	_ = winapi.AddUninstallEntry(uninstallID, "Remote Support Manager agent", agentcore.Version, dst, `"`+dst+`" uninstall`)
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func removeAutostart() error { return winapi.SetRunAtSignIn(runName, "", false) }

func uninstall() error {
	_ = removeAutostart()
	winapi.RemoveUninstallEntry(uninstallID)
	// The running executable cannot delete itself; schedule removal of the
	// folder for the next reboot is not used. Leave the file and tell the user.
	return nil
}

func startDetached(exe string) error {
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 /* DETACHED_PROCESS */}
	return cmd.Start()
}

var mutex windows.Handle

func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\RemoteSupportManagerAgent`)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		return false
	}
	mutex = h
	return true
}

func setQuit(p agentcore.Platform, stop context.CancelFunc) {
	if wp, ok := p.(*winPlatform); ok {
		wp.onQuit = stop
	}
}

// setUpdater wires the tray's "Check for updates…" item. Updates come from
// the agent's own server (pinned TLS), are signed, need the person at the PC
// to approve download and install, and never install during a session.
func setUpdater(p agentcore.Platform, a *agentcore.Agent, stop context.CancelFunc) {
	wp, ok := p.(*winPlatform)
	if !ok {
		return
	}
	exe, _ := os.Executable()
	flow := &updateui.Flow{
		UI:    wp.ui,
		Owner: func() windows.HWND { return wp.owner },
		Title: appTitle,
		Checker: &update.Checker{Product: "agent", Current: agentcore.Version, PublicKey: update.PublicKey,
			FeedURL: strings.TrimRight(a.Cfg.Server, "/") + "/updates/agent-manifest.json", TLS: tlspin.Config(a.Cfg.Pin)},
		Exe:  exe,
		Busy: a.Busy,
		Restart: func(path string) {
			if mutex != 0 {
				windows.CloseHandle(mutex)
				mutex = 0
			}
			_ = startDetached(path)
			stop()
		},
	}
	wp.onUpdate = func() { flow.CheckForUpdates(true) }
}

// confirmStartup completes or rolls back an earlier update. It returns true
// when this process must exit (the restored version was started).
func confirmStartup() bool {
	exe, _ := os.Executable()
	return updateui.ConfirmStartup(exe, agentcore.Version, func(p string) { _ = startDetached(p) })
}

func markHealthy() {
	exe, _ := os.Executable()
	updateui.Healthy(exe)
}
