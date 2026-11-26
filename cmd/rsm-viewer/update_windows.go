//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
	"github.com/TechSavage1989/remote-support-manager/internal/update"
	"github.com/TechSavage1989/remote-support-manager/internal/updateui"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const releasesURL = "https://github.com/TechSavage1989/remote-support-manager/releases"

// updateUI connects the shared update flow and card to the viewer window.
type updateUI struct {
	a    *app
	flow *updateui.Flow
	card *updateui.Card
}

func (u *updateUI) busy() bool { return u.flow.BusyDownloading() }

func (u *updateUI) feed() (string, string) {
	s := u.a.set
	if s.UpdateFeed != "" {
		return s.UpdateFeed, s.LastPin
	}
	if s.LastServer != "" {
		return strings.TrimRight(s.LastServer, "/") + "/updates/viewer-manifest.json", s.LastPin
	}
	return "", ""
}

// checkNow is Help > App updates: it opens the settings section and checks.
func (u *updateUI) checkNow(manual bool) {
	if manual {
		updateui.ShowSettings(u.a.main, u.flow, releasesURL)
	}
	u.flow.Check(manual)
}

func (u *updateUI) relayout() {
	if u.card != nil {
		u.card.Update(u.flow.State())
	}
}

// startUpdates finishes or rolls back an earlier install and starts the
// automatic checks. Checks only report; nothing downloads or restarts until
// the user presses Download update and then Install and restart.
func (a *app) startUpdates() {
	exe, _ := os.Executable()
	if updateui.ConfirmStartup(exe, Version, func(p string) { _ = exec.Command(p).Start() }) {
		os.Exit(0)
	}
	u := &updateUI{a: a}
	u.flow = &updateui.Flow{
		UI:      a.ui,
		Owner:   func() windows.HWND { return a.main },
		AppName: title,
		Checker: &update.Checker{Product: "viewer", Current: Version, PublicKey: update.PublicKey},
		Exe:     exe,
		Busy: func() bool {
			c := a.c
			return c != nil && !a.ended && c.State() != "ended"
		},
		Configure: func(c *update.Checker) bool {
			url, pin := u.feed()
			c.FeedURL, c.TLS = url, tlspin.Config(pin)
			return url != ""
		},
		Restart: func(path string) {
			_ = exec.Command(path).Start()
			a.ui.Post(func() {
				a.ended = true // Install already refused if a session was active
				winapi.DestroyWindow(a.main)
			})
		},
		SkipVersion:  func() string { return a.set.SkipVersion },
		SetSkip:      func(v string) { a.set.SkipVersion = v; a.set.save() },
		AutoCheck:    func() bool { return a.set.AutoCheck == nil || *a.set.AutoCheck },
		SetAutoCheck: func(on bool) { a.set.AutoCheck = &on; a.set.save() },
	}
	a.upd = u
	a.ui.Do(func() {
		u.card = updateui.NewCard(a.main, u.flow, false)
		u.card.OnDetails = func() { updateui.ShowSettings(a.main, u.flow, releasesURL) }
		u.card.PlaceBottomRight(a.main, 0)
		u.flow.OnChange = u.card.Update
		u.flow.Start()
	})
	updateui.Healthy(exe)
}
