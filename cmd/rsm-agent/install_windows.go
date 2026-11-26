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
	"time"

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

// removeUserInstall removes the per-user client: sign-in entry, the
// Settings > Apps entry, its settings and credential, and the program
// folder. self is true when the running client removes itself (support
// session deleted, device removed); otherwise a running client is asked
// to stop first.
func removeUserInstall(self bool) {
	if !self {
		stopRunningClient()
	}
	_ = removeAutostart()
	winapi.RemoveUninstallEntry(uninstallID)
	if path, err := agentcore.ConfigPath(); err == nil {
		dir := filepath.Dir(path)
		for _, f := range []string{"agent.json", "agent.json.tmp", "agent.log", "agent.log.old"} {
			_ = os.Remove(filepath.Join(dir, f))
		}
		_ = os.Remove(dir) // only if empty (the viewer keeps its settings here too)
	}
	selfDelete(winapi.ProgramsDir("Remote Support Manager"))
}

// selfDelete removes dir once this process has exited.
func selfDelete(dir string) { winapi.RemoveDirLater(dir) }

func uninstall() error {
	removeUserInstall(false)
	return nil
}

func startDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 /* DETACHED_PROCESS */}
	return cmd.Start()
}

var mutex windows.Handle

const stopEventName = `Local\RemoteSupportManagerAgentStop`

func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\RemoteSupportManagerAgent`)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		return false
	}
	mutex = h
	return true
}

// stopRunningClient asks this user's running client to exit (uninstall
// from Settings > Apps) and waits briefly for it.
func stopRunningClient() {
	n, _ := windows.UTF16PtrFromString(stopEventName)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, n)
	if err != nil {
		return
	}
	_ = windows.SetEvent(h)
	windows.CloseHandle(h)
	time.Sleep(2 * time.Second)
}

func setQuit(p agentcore.Platform, stop context.CancelFunc) {
	if wp, ok := p.(*winPlatform); ok {
		wp.onQuit = stop
	}
	n, _ := windows.UTF16PtrFromString(stopEventName)
	h, err := windows.CreateEvent(nil, 1, 0, n)
	if err != nil {
		return
	}
	go func() {
		if ev, _ := windows.WaitForSingleObject(h, windows.INFINITE); ev == windows.WAIT_OBJECT_0 {
			stop()
		}
	}()
}

// setUpdater starts the shared update flow for the agent. Updates come from
// the agent's own server over pinned TLS and must be signed. Checks run
// automatically and only report (a tray notification); the person at the PC
// downloads and installs from the tray's "App updates…" window, and install
// is refused while a support request or session is active.
func setUpdater(p agentcore.Platform, a *agentcore.Agent, stop context.CancelFunc) {
	wp, ok := p.(*winPlatform)
	if !ok {
		return
	}
	exe, _ := os.Executable()
	flow := &updateui.Flow{
		UI:      wp.ui,
		Owner:   func() windows.HWND { return wp.owner },
		AppName: appTitle + " agent",
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
	announced := ""
	flow.OnChange = func(s updateui.State) {
		if s.Phase == updateui.Available && s.M != nil && announced != s.M.Version {
			announced = s.M.Version
			wp.tray.Balloon(appTitle, "Agent update "+s.M.Version+" is available. Open the tray icon and choose App updates to review it.")
		}
	}
	wp.onUpdate = func() {
		updateui.ShowSettings(0, flow, "https://github.com/TechSavage1989/remote-support-manager/releases")
	}
	wp.ui.Do(flow.Start)
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
