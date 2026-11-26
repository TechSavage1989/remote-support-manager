//go:build windows

// Package updateui is the Windows user interface for the updater, shared by
// the viewer and the agent so both follow the same rules:
//
//   - Checks run automatically (shortly after start, every five minutes
//     while the app runs, and when the window regains focus at least two
//     minutes after the last check). A check only reports.
//   - Download and Install-and-restart run only after the user presses the
//     matching button. Nothing downloads or restarts on its own.
//   - Installing is refused while a support session is active.
//   - A failed or offline check shows "Update needs attention", never
//     "You are up to date".
//
// Drawing functions (reuse these instead of building a second update UI):
// RenderUpdateCard, RenderWhatsNew, RenderDownloadBar and ReleaseNoteLines in
// card_windows.go; ShowReviewDialog for the install review; ShowSettings for
// the "App updates" section.
package updateui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/update"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

// Phase is the update state shown on the card.
type Phase int

const (
	Idle Phase = iota
	Checking
	Available
	Downloading
	Verifying
	Ready
	UpToDate
	Failed
	Installing
)

// State is everything the card needs to draw itself.
type State struct {
	Phase     Phase
	AppName   string
	Installed string
	M         *update.Manifest
	Done      int64
	Total     int64
	Err       string
	LastCheck time.Time
	NextCheck time.Time
	Postponed bool
	AutoCheck bool
	Signed    bool // a signing key is compiled in
}

// Title is the card title for the state.
func (s State) Title() string {
	switch s.Phase {
	case Checking:
		return "Checking for updates"
	case Available:
		return "Update available"
	case Downloading:
		return "Downloading update"
	case Verifying:
		return "Verifying update"
	case Ready:
		return "Ready to install"
	case UpToDate:
		return "You are up to date"
	case Failed:
		return "Update needs attention"
	case Installing:
		return "Installing update"
	}
	return "App updates"
}

// Sentence says what happens next.
func (s State) Sentence() string {
	switch s.Phase {
	case Checking:
		return "Looking for a newer version on your support server."
	case Available:
		return "Download now and choose when to restart."
	case Downloading:
		return "You can keep working. Nothing installs until you choose."
	case Verifying:
		return "Checking the download against the signed release information."
	case Ready:
		return "Review the update, then restart when it suits you."
	case UpToDate:
		return "Updates are checked automatically while the app is open."
	case Failed:
		return s.Err
	case Installing:
		return "The app closes, installs the verified update and reopens."
	}
	if !s.Signed {
		return "Updates are turned off in this build because no signing key was built in."
	}
	return "Updates come from your Remote Support Manager server."
}

const (
	checkEvery  = 5 * time.Minute
	focusMinGap = 2 * time.Minute
	timerID     = 77
)

// Flow drives one app's updates.
type Flow struct {
	UI      *winapi.UI
	Owner   func() windows.HWND
	AppName string
	Checker *update.Checker
	Exe     string
	// Busy reports an active support session (install is refused).
	Busy func() bool
	// Restart starts the installed program and closes this one.
	Restart func(exe string)
	// Configure is called before each check to refresh the feed URL and TLS
	// settings; it returns false when no update source is known yet.
	Configure func(c *update.Checker) bool
	// SkipVersion / SetSkip persist "Later" for one version.
	SkipVersion func() string
	SetSkip     func(string)
	// AutoCheck / SetAutoCheck persist the automatic-check preference.
	AutoCheck    func() bool
	SetAutoCheck func(bool)
	// OnChange is called on the UI thread after every state change.
	OnChange func(State)

	mu     sync.Mutex
	st     State
	staged string
	cancel context.CancelFunc
	timer  windows.HWND
	subs   []func(State)
}

// Start sets up the native timer and schedules the first check. Call it on
// the UI thread after the main window exists.
func (f *Flow) Start() {
	f.st = State{AppName: f.AppName, Installed: f.Checker.Current, Signed: f.Checker.PublicKey != "", AutoCheck: f.auto()}
	winapi.RegisterClass("RSMUpdateTimer", 0)
	f.timer = winapi.CreateWindow(0, "RSMUpdateTimer", "", 0, 0, 0, 0, 0, windows.HWND(^uintptr(2)), 0,
		func(h windows.HWND, m uint32, wp, lp uintptr) (uintptr, bool) {
			if m == winapi.WM_TIMER && wp == timerID {
				winapi.KillTimer(h, timerID)
				winapi.SetTimer(h, timerID, uintptr(checkEvery/time.Millisecond))
				if f.auto() {
					f.Check(false)
				}
				return 0, true
			}
			return 0, false
		})
	// First check a few seconds after start so startup is never delayed.
	winapi.SetTimer(f.timer, timerID, 4000)
	f.emit()
}

func (f *Flow) auto() bool { return f.AutoCheck == nil || f.AutoCheck() }

// Subscribe adds another view (for example the settings window).
func (f *Flow) Subscribe(fn func(State)) (cancel func()) {
	f.mu.Lock()
	f.subs = append(f.subs, fn)
	i := len(f.subs) - 1
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.subs[i] = nil
		f.mu.Unlock()
	}
}

// State returns a copy of the current state.
func (f *Flow) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *Flow) set(fn func(s *State)) {
	f.mu.Lock()
	fn(&f.st)
	f.st.AutoCheck = f.auto()
	f.mu.Unlock()
	f.UI.Post(f.emit)
}

func (f *Flow) emit() {
	s := f.State()
	if f.OnChange != nil {
		f.OnChange(s)
	}
	f.mu.Lock()
	subs := make([]func(State), len(f.subs))
	copy(subs, f.subs)
	f.mu.Unlock()
	for _, fn := range subs {
		if fn != nil {
			fn(s)
		}
	}
}

// FocusReturned triggers a check when the window regains focus at least two
// minutes after the last one.
func (f *Flow) FocusReturned() {
	s := f.State()
	if f.auto() && !s.LastCheck.IsZero() && time.Since(s.LastCheck) >= focusMinGap {
		f.Check(false)
	}
}

// BusyDownloading reports whether a check or download is running.
func (f *Flow) BusyDownloading() bool { return f.Checker.Busy() }

// Check checks the signed manifest. manual=true is the "Check for updates"
// button: it ignores backoff and clears a postponed version.
func (f *Flow) Check(manual bool) {
	if f.Checker.PublicKey == "" {
		if manual {
			f.set(func(s *State) { s.Phase, s.Err = Failed, update.ErrNoKey.Error() })
		}
		return
	}
	if f.Configure != nil && !f.Configure(f.Checker) {
		if manual {
			f.set(func(s *State) {
				s.Phase, s.Err = Failed, "Connect to a session once so the app knows your support server, then check again."
			})
		}
		return
	}
	cur := f.State().Phase
	if cur == Downloading || cur == Verifying || cur == Installing || (cur == Ready && !manual) {
		return
	}
	f.set(func(s *State) {
		if manual || s.Phase == Idle || s.Phase == Failed {
			s.Phase = Checking
		}
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		m, err := f.Checker.Check(ctx, manual)
		now := time.Now()
		if errors.Is(err, update.ErrInProgress) || errors.Is(err, update.ErrBackoff) {
			f.set(func(s *State) {
				if s.Phase == Checking {
					s.Phase = Idle
				}
			})
			return
		}
		f.set(func(s *State) {
			s.LastCheck, s.NextCheck = now, now.Add(checkEvery)
			switch {
			case err != nil:
				s.Phase, s.Err = Failed, "Could not check for updates: "+err.Error()
			case m == nil:
				s.Phase, s.M, s.Postponed = UpToDate, nil, false
			default:
				if s.Phase == Ready && s.M != nil && s.M.Version == m.Version {
					return // already downloaded and verified
				}
				if s.M == nil || s.M.Version != m.Version {
					s.Done, s.Total = 0, m.Size
				}
				s.M = m
				s.Phase = Available
				skip := ""
				if f.SkipVersion != nil {
					skip = f.SkipVersion()
				}
				if manual && skip == m.Version && f.SetSkip != nil {
					f.SetSkip("")
					skip = ""
				}
				s.Postponed = skip == m.Version
			}
		})
	}()
}

// Download downloads and verifies the available update (user action).
func (f *Flow) Download() {
	s := f.State()
	if s.M == nil || s.Phase == Downloading || s.Phase == Verifying {
		return
	}
	m := s.M
	dir := filepath.Join(filepath.Dir(f.Exe), "updates")
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		dir = filepath.Join(la, "RemoteSupportManager", "updates", m.Product)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	f.cancel = cancel
	f.mu.Unlock()
	f.set(func(s *State) { s.Phase, s.Done, s.Total, s.Err = Downloading, 0, m.Size, "" })
	go func() {
		defer cancel()
		path, err := f.Checker.Download(ctx, m, dir, func(done, total int64) {
			f.set(func(s *State) {
				s.Done, s.Total = done, total
				if done >= total {
					s.Phase = Verifying
				}
			})
		})
		if err != nil {
			f.set(func(s *State) { s.Phase, s.Err = Failed, "The update was not downloaded: "+err.Error() })
			return
		}
		f.mu.Lock()
		f.staged = path
		f.mu.Unlock()
		f.set(func(s *State) { s.Phase, s.Done = Ready, s.Total })
	}()
}

// Later postpones this version until the next manual check.
func (f *Flow) Later() {
	s := f.State()
	if s.M != nil && f.SetSkip != nil {
		f.SetSkip(s.M.Version)
	}
	f.set(func(s *State) { s.Postponed = true })
}

// SetAuto changes the automatic-check preference.
func (f *Flow) SetAuto(on bool) {
	if f.SetAutoCheck != nil {
		f.SetAutoCheck(on)
	}
	f.set(func(s *State) {})
}

// Install installs the staged update and restarts. It returns an error for
// the review dialog to show inline; the dialog stays open for a retry.
func (f *Flow) Install() error {
	f.mu.Lock()
	path := f.staged
	m := f.st.M
	f.mu.Unlock()
	if path == "" || m == nil {
		return errors.New("download the update first")
	}
	if f.Busy != nil && f.Busy() {
		return update.ErrBusy
	}
	if err := update.Install(f.Exe, path, f.Checker.Current, m.Version, f.Busy); err != nil {
		return err
	}
	f.mu.Lock()
	f.staged = ""
	f.mu.Unlock()
	f.set(func(s *State) { s.Phase = Installing })
	if f.Restart != nil {
		f.Restart(f.Exe)
	}
	return nil
}

// Cancel stops a running download (the partial file is kept for resume).
func (f *Flow) Cancel() {
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
	}
	f.mu.Unlock()
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

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
