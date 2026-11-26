//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
	"github.com/TechSavage1989/remote-support-manager/internal/update"
	"github.com/TechSavage1989/remote-support-manager/internal/updateui"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

// updateUI connects the shared update flow to the viewer window.
type updateUI struct {
	a    *app
	flow *updateui.Flow
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

func (u *updateUI) checkNow(manual bool) {
	url, pin := u.feed()
	u.flow.Checker.FeedURL = url
	u.flow.Checker.TLS = tlspin.Config(pin)
	if manual && url == "" {
		winapi.MessageBox(u.a.main, "Updates come from your Remote Support Manager server. Connect to a session once so the viewer knows the server.", title, winapi.MB_ICONINFORMATION)
		return
	}
	u.flow.CheckForUpdates(manual)
}

// startUpdates finishes or rolls back an earlier install and schedules the
// once-a-day automatic check (check only; never downloads).
func (a *app) startUpdates() {
	exe, _ := os.Executable()
	restart := func(path string) {
		cmd := exec.Command(path)
		_ = cmd.Start()
		a.ui.Post(func() {
			a.ended = true // nothing to confirm: Install refused if a session was active
			winapi.DestroyWindow(a.main)
		})
	}
	if updateui.ConfirmStartup(exe, Version, func(p string) { _ = exec.Command(p).Start() }) {
		os.Exit(0)
	}
	u := &updateUI{a: a}
	u.flow = &updateui.Flow{
		UI:      a.ui,
		Owner:   func() windows.HWND { return a.main },
		Title:   title,
		Checker: &update.Checker{Product: "viewer", Current: Version, PublicKey: update.PublicKey},
		Exe:     exe,
		Busy: func() bool {
			c := a.c
			return c != nil && !a.ended && c.State() != "ended"
		},
		Status:      a.setStatus,
		Restart:     restart,
		SkipVersion: func() string { return a.set.SkipVersion },
		SetSkip:     func(v string) { a.set.SkipVersion = v; a.set.save() },
	}
	a.upd = u
	updateui.Healthy(exe)
	auto := a.set.AutoCheck == nil || *a.set.AutoCheck
	if auto && time.Since(time.Unix(a.set.LastCheck, 0)) > 24*time.Hour {
		if url, _ := u.feed(); url != "" {
			a.set.LastCheck = time.Now().Unix()
			a.set.save()
			u.checkNow(false)
		}
	}
}
