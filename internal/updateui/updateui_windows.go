//go:build windows

// Package updateui is the Windows user interface for the updater, shared by
// the viewer and the agent so both follow the same rules:
//
//   - CheckForUpdates may run automatically (at most once a day) but only
//     reports; DownloadUpdate and InstallAndRestart run only after the user
//     presses the matching button.
//   - InstallAndRestart refuses while a support session is active.
//   - ConfirmStartup must be called once the app has started; it completes
//     or rolls back an earlier install.
package updateui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/update"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const mbYesNoCancel = 0x3

// Flow drives one app's updates.
type Flow struct {
	UI      *winapi.UI
	Owner   func() windows.HWND
	Title   string
	Checker *update.Checker
	Exe     string
	// Busy reports an active support session.
	Busy func() bool
	// Status shows short progress text (may be nil).
	Status func(string)
	// Restart starts the installed program and closes this one.
	Restart func(exe string)
	// SkipVersion / SetSkip persist "skip this version".
	SkipVersion func() string
	SetSkip     func(string)

	mu      sync.Mutex
	staged  string
	stagedM *update.Manifest
	cancel  context.CancelFunc
}

func (f *Flow) status(s string) {
	if f.Status != nil {
		f.UI.Post(func() { f.Status(s) })
	}
}

func (f *Flow) box(text string, flags uint32) int {
	var r int
	f.UI.Do(func() { r = winapi.MessageBox(f.Owner(), text, f.Title, flags|winapi.MB_SETFOREGROUND) })
	return r
}

// ConfirmStartup completes an earlier install. It returns true if the app
// rolled back and must exit immediately (the restored version is started).
func ConfirmStartup(exe, version string, restart func(string)) bool {
	update.Cleanup(exe)
	rolled, err := update.StartupCheck(exe, version)
	if rolled {
		msg := "The updated version did not start correctly, so the previous version was restored."
		if err != nil {
			msg = "The updated version did not start correctly and restoring the previous version failed: " + err.Error() + "\n\nReinstall from your support server."
		}
		winapi.MessageBox(0, msg, "Remote Support Manager", winapi.MB_ICONWARNING)
		if err == nil && restart != nil {
			restart(exe)
		}
		return true
	}
	return false
}

// Healthy marks this start as good. Call it after the main window exists.
func Healthy(exe string) { update.ConfirmHealthy(exe) }

// BusyDownloading reports whether a check or download is running.
func (f *Flow) BusyDownloading() bool { return f.Checker.Busy() }

// CheckForUpdates checks the signed manifest. When manual is false it only
// updates the status text; when true it walks the user through the
// approved steps.
func (f *Flow) CheckForUpdates(manual bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if manual {
			f.status("Checking for updates…")
		}
		m, err := f.Checker.Check(ctx, manual)
		if err != nil {
			if manual {
				f.status("")
				f.box("Could not check for updates: "+err.Error(), winapi.MB_ICONWARNING)
			}
			return
		}
		if m == nil {
			if manual {
				f.status("")
				f.box("You have the latest version ("+f.Checker.Current+").", winapi.MB_ICONINFORMATION)
			}
			return
		}
		if !manual {
			if f.SkipVersion != nil && f.SkipVersion() == m.Version {
				return
			}
			f.status(fmt.Sprintf("Version %s is available. Choose Check for updates to download it.", m.Version))
			return
		}
		f.offer(m)
	}()
}

func (f *Flow) offer(m *update.Manifest) {
	notes := m.Notes
	if notes == "" {
		notes = "No release notes were provided."
	}
	r := f.box(fmt.Sprintf("Version %s is available (you have %s, download %.1f MB).\n\n%s\n\nDownload it now?\n\nYes: download and verify (nothing is installed yet)\nNo: not now\nCancel: skip this version",
		m.Version, f.Checker.Current, float64(m.Size)/(1<<20), notes), mbYesNoCancel|winapi.MB_ICONQUESTION)
	switch r {
	case winapi.IDYES:
		f.DownloadUpdate(m)
	case winapi.IDCANCEL:
		if f.SetSkip != nil {
			f.SetSkip(m.Version)
		}
		f.status("")
	default:
		f.status("")
	}
}

// DownloadUpdate downloads and verifies m, then offers InstallAndRestart.
func (f *Flow) DownloadUpdate(m *update.Manifest) {
	dir := filepath.Join(filepath.Dir(f.Exe), "updates")
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		dir = filepath.Join(la, "RemoteSupportManager", "updates", m.Product)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	f.cancel = cancel
	f.mu.Unlock()
	path, err := f.Checker.Download(ctx, m, dir, func(done, total int64) {
		f.status(fmt.Sprintf("Downloading update %s: %d%%", m.Version, done*100/total))
	})
	cancel()
	if err != nil {
		f.status("")
		f.box("The update was not downloaded: "+err.Error(), winapi.MB_ICONWARNING)
		return
	}
	f.mu.Lock()
	f.staged, f.stagedM = path, m
	f.mu.Unlock()
	f.status(fmt.Sprintf("Update %s downloaded and verified. It installs only when you choose.", m.Version))
	f.InstallAndRestart()
}

// InstallAndRestart installs the staged update after asking, never during
// a session.
func (f *Flow) InstallAndRestart() {
	f.mu.Lock()
	path, m := f.staged, f.stagedM
	f.mu.Unlock()
	if path == "" {
		return
	}
	if f.Busy != nil && f.Busy() {
		f.box("Update "+m.Version+" is ready, but a support session is active. It will not interrupt the session.\n\nChoose Check for updates again after the session ends to install it.", winapi.MB_ICONINFORMATION)
		return
	}
	if f.box("Install version "+m.Version+" and restart now?\n\nThe current version is kept as a backup and restored automatically if the new one fails to start.", winapi.MB_YESNO|winapi.MB_ICONQUESTION) != winapi.IDYES {
		return
	}
	if err := update.Install(f.Exe, path, f.Checker.Current, m.Version, f.Busy); err != nil {
		f.box("The update was not installed: "+err.Error(), winapi.MB_ICONERROR)
		return
	}
	f.mu.Lock()
	f.staged, f.stagedM = "", nil
	f.mu.Unlock()
	if f.Restart != nil {
		f.Restart(f.Exe)
	}
}

// Cancel stops a running download (the partial file is kept for resume).
func (f *Flow) Cancel() {
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
	}
	f.mu.Unlock()
}
