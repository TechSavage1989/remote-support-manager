//go:build windows

package main

import (
	"context"
	"fmt"
	"image"
	"strings"
	"sync"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const appTitle = "Remote Support Manager"

type winPlatform struct {
	ui    *winapi.UI
	owner windows.HWND
	tray  *winapi.Tray
	cap   winapi.Capturer
	capMu sync.Mutex

	clipMu   sync.Mutex
	clipSubs map[int]func()
	clipN    int
	onQuit   func()
	onUpdate func()
}

func newPlatform() (agentcore.Platform, func(), error) {
	winapi.EnableDPIAwareness()
	winapi.InitCommonControls()
	p := &winPlatform{clipSubs: map[int]func(){}}
	p.ui = winapi.NewUI(func(ui *winapi.UI) {
		winapi.RegisterClass("RSMAgentOwner", 0)
		p.owner = winapi.CreateWindow(0, "RSMAgentOwner", appTitle, 0, 0, 0, 0, 0, 0, 0, p.ownerProc)
		winapi.ClipboardListener(p.owner, true)
		p.tray = winapi.NewTray(p.owner, appTitle+" agent: waiting for support requests")
	})
	cleanup := func() {
		p.ui.Do(func() {
			p.tray.Remove()
			winapi.ClipboardListener(p.owner, false)
		})
	}
	return p, cleanup, nil
}

const (
	trayStatus = 1
	trayQuit   = 2
	trayUpdate = 3
)

func (p *winPlatform) ownerProc(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case winapi.WM_CLIPBOARDUPDATE:
		p.clipMu.Lock()
		subs := make([]func(), 0, len(p.clipSubs))
		for _, f := range p.clipSubs {
			subs = append(subs, f)
		}
		p.clipMu.Unlock()
		for _, f := range subs {
			go f()
		}
		return 0, true
	case winapi.TrayMessage:
		if lp == winapi.WM_RBUTTONUP || lp == winapi.WM_LBUTTONUP {
			m := winapi.CreatePopupMenu()
			winapi.AppendMenu(m, winapi.MF_STRING|winapi.MF_GRAYED, trayStatus, appTitle+" agent "+agentcore.Version)
			winapi.AppendMenu(m, winapi.MF_SEPARATOR, 0, "")
			winapi.AppendMenu(m, winapi.MF_STRING, trayUpdate, "Check for updates…")
			winapi.AppendMenu(m, winapi.MF_STRING, trayQuit, "Stop the agent until next sign-in")
			pt := winapi.CursorPos()
			winapi.SetForeground(h)
			switch winapi.TrackPopup(m, h, pt.X, pt.Y) {
			case trayQuit:
				if p.onQuit != nil {
					p.onQuit()
				}
			case trayUpdate:
				if p.onUpdate != nil {
					p.onUpdate()
				}
			}
		}
		return 0, true
	}
	return 0, false
}

func (p *winPlatform) Inventory() agentcore.Inventory {
	name, ver := winapi.OSVersion()
	inv := agentcore.Inventory{OSName: name, OSVersion: ver, UptimeSec: winapi.UptimeSeconds(), LoggedOnUser: winapi.CurrentUser()}
	inv.Hostname, _ = hostname()
	for _, d := range winapi.FixedDisks() {
		inv.Disks = append(inv.Disks, agentcore.Disk{Name: d.Name, Total: d.Total, Free: d.Free})
	}
	return inv
}

func (p *winPlatform) Monitors() []protocol.Monitor { return winapi.Monitors() }

func (p *winPlatform) Capture(m protocol.Monitor) (*image.RGBA, error) {
	p.capMu.Lock()
	defer p.capMu.Unlock()
	return p.cap.Capture(m)
}

func (p *winPlatform) InjectMouse(m protocol.Mouse, mon protocol.Monitor) error {
	return winapi.InjectMouse(m, mon)
}
func (p *winPlatform) InjectKey(k protocol.Key) error { return winapi.InjectKey(k) }
func (p *winPlatform) InjectText(t string) error      { return winapi.InjectText(t) }
func (p *winPlatform) SecureAttention() error         { return winapi.SendSAS() }

func (p *winPlatform) ReadClipboard() (clipsync.Clip, error) {
	var c clipsync.Clip
	var err error
	p.ui.Do(func() { c, err = winapi.ReadClipboard(p.owner) })
	return c, err
}

func (p *winPlatform) WriteClipboard(c clipsync.Clip) error {
	var err error
	p.ui.Do(func() { err = winapi.WriteClipboard(p.owner, c) })
	return err
}

func (p *winPlatform) WatchClipboard(fn func()) func() {
	p.clipMu.Lock()
	id := p.clipN
	p.clipN++
	p.clipSubs[id] = fn
	p.clipMu.Unlock()
	return func() {
		p.clipMu.Lock()
		delete(p.clipSubs, id)
		p.clipMu.Unlock()
	}
}

func (p *winPlatform) MarkReceived(path string) { winapi.MarkOfTheWeb(path) }

func (p *winPlatform) Roots() []agentcore.FileEntry {
	out := []agentcore.FileEntry{}
	for _, f := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Desktop, windows.FOLDERID_Downloads, windows.FOLDERID_Documents} {
		if path := winapi.KnownFolder(f); path != "" {
			out = append(out, agentcore.FileEntry{Name: path, Dir: true})
		}
	}
	for _, d := range winapi.FixedDisks() {
		out = append(out, agentcore.FileEntry{Name: d.Name + `\`, Dir: true, Size: int64(d.Free)})
	}
	return out
}

func (p *winPlatform) Notify(text string) {
	p.ui.Post(func() { p.tray.Balloon(appTitle, text) })
}

func (p *winPlatform) Protect(b []byte) ([]byte, error)   { return winapi.Protect(b) }
func (p *winPlatform) Unprotect(b []byte) ([]byte, error) { return winapi.Unprotect(b) }

// ------------------------------------------------------------ consent window

var permLabels = map[string]string{
	"view":      "See your screen (required)",
	"control":   "Control your mouse and keyboard",
	"clipboard": "Share clipboard text and images",
	"transfer":  "Send and receive files",
}

const (
	idAllow   = 101
	idDecline = 102
	idCode    = 103
	idPerm    = 110
)

func (p *winPlatform) AskConsent(ctx context.Context, req protocol.SessionRequest, attempt int) agentcore.ConsentResult {
	result := make(chan agentcore.ConsentResult, 1)
	var hwnd windows.HWND
	p.ui.Do(func() {
		winapi.RegisterClass("RSMConsent", uintptr(winapi.COLOR_BTNFACE+1))
		checks := map[string]windows.HWND{}
		var code windows.HWND
		finish := func(r agentcore.ConsentResult) {
			select {
			case result <- r:
			default:
			}
			winapi.UnregisterDialog(hwnd)
			winapi.DestroyWindow(hwnd)
		}
		w, h := int32(500), int32(400)
		sw, sh := winapi.SystemMetric(winapi.SM_CXSCREEN), winapi.SystemMetric(winapi.SM_CYSCREEN)
		hwnd = winapi.CreateWindow(winapi.WS_EX_TOPMOST|winapi.WS_EX_DLGMODALFRAME, "RSMConsent", "Remote support request",
			winapi.WS_CAPTION|winapi.WS_SYSMENU|winapi.WS_VISIBLE, (sw-w)/2, (sh-h)/2, w, h, 0, 0,
			func(hh windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
				switch msg {
				case winapi.WM_COMMAND:
					switch wp & 0xffff {
					case idAllow:
						perms := []string{}
						for _, k := range req.Perms {
							if c := checks[k]; c != 0 && winapi.Checked(c) {
								perms = append(perms, k)
							}
						}
						entered := strings.ReplaceAll(strings.TrimSpace(winapi.GetText(code)), " ", "")
						if len(entered) != 6 {
							winapi.MessageBox(hh, "Enter the 6-digit code your technician reads to you.", "Remote support", winapi.MB_ICONWARNING)
							winapi.SetFocus(code)
							return 0, true
						}
						finish(agentcore.ConsentResult{Accept: true, Code: entered, Perms: perms})
					case idDecline, winapi.IDCANCEL:
						finish(agentcore.ConsentResult{Accept: false})
					}
					return 0, true
				case winapi.WM_CLOSE:
					finish(agentcore.ConsentResult{Accept: false})
					return 0, true
				}
				return 0, false
			})
		title := winapi.Control(hwnd, "STATIC", fmt.Sprintf("%s wants to connect to this PC", req.Technician), 0, 20, 16, 450, 26, 0)
		winapi.SetFont(title, winapi.BoldFont(18))
		winapi.Control(hwnd, "STATIC", fmt.Sprintf("%s · %s", req.TechEmail, req.Org), 0, 20, 44, 450, 20, 0)
		winapi.Control(hwnd, "STATIC", "Only allow this if you asked for help. They are asking to:", 0, 20, 74, 450, 20, 0)
		y := int32(98)
		for i, k := range req.Perms {
			c := winapi.Control(hwnd, "BUTTON", permLabels[k], winapi.BS_AUTOCHECKBOX|winapi.WS_TABSTOP, 36, y, 420, 22, idPerm+i)
			winapi.SetChecked(c, true)
			if k == "view" {
				winapi.Enable(c, false)
			}
			checks[k] = c
			y += 24
		}
		y += 10
		winapi.Control(hwnd, "STATIC", "Type the 6-digit support code your technician gives you:", 0, 20, y, 450, 20, 0)
		code = winapi.ControlEx(hwnd, winapi.WS_EX_CLIENTEDGE, "EDIT", "", winapi.ES_NUMBER|winapi.WS_TABSTOP, 20, y+24, 140, 28, idCode)
		winapi.SetFont(code, winapi.BoldFont(20))
		winapi.SendMessage(code, winapi.EM_SETLIMITTEXT, 6, 0)
		if attempt > 1 {
			winapi.Control(hwnd, "STATIC", "That code was not correct. Check it with your technician.", 0, 172, y+30, 300, 20, 0)
		}
		y += 66
		winapi.Control(hwnd, "STATIC", "You will see a bar at the top of the screen while connected, and you can end the session at any time.", 0, 20, y, 450, 36, 0)
		winapi.Control(hwnd, "BUTTON", "Allow", winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, w-220, h-80, 90, 30, idAllow)
		winapi.Control(hwnd, "BUTTON", "Decline", winapi.WS_TABSTOP, w-120, h-80, 90, 30, idDecline)
		winapi.RegisterDialog(hwnd)
		winapi.SetForeground(hwnd)
		winapi.SetFocus(code)
	})
	select {
	case r := <-result:
		return r
	case <-ctx.Done():
		p.ui.Do(func() {
			if winapi.IsWindow(hwnd) {
				winapi.UnregisterDialog(hwnd)
				winapi.DestroyWindow(hwnd)
			}
		})
		return agentcore.ConsentResult{}
	}
}

// ------------------------------------------------------------ indicator

type winIndicator struct {
	p      *winPlatform
	hwnd   windows.HWND
	label  windows.HWND
	mu     sync.Mutex
	perms  []string
	tech   string
	status string
	hooks  agentcore.IndicatorHooks
}

const (
	idEnd   = 201
	idPerms = 202
	idPermC = 210
)

func (p *winPlatform) ShowIndicator(tech, email string, perms []string, hooks agentcore.IndicatorHooks) agentcore.Indicator {
	ind := &winIndicator{p: p, perms: perms, tech: tech, status: "connected", hooks: hooks}
	p.ui.Do(func() {
		winapi.RegisterClass("RSMIndicator", amberBrush())
		sw := winapi.SystemMetric(winapi.SM_CXSCREEN)
		w, h := int32(640), int32(40)
		ind.hwnd = winapi.CreateWindow(winapi.WS_EX_TOPMOST|winapi.WS_EX_TOOLWINDOW, "RSMIndicator", appTitle+": session active",
			winapi.WS_POPUP|winapi.WS_VISIBLE|winapi.WS_BORDER, (sw-w)/2, 0, w, h, 0, 0, ind.proc)
		ind.label = winapi.Control(ind.hwnd, "STATIC", "", 0, 10, 11, 410, 20, 0)
		winapi.Control(ind.hwnd, "BUTTON", "Permissions…", 0, 428, 6, 100, 28, idPerms)
		winapi.Control(ind.hwnd, "BUTTON", "End session", winapi.BS_DEFPUSHBUTTON, 534, 6, 98, 28, idEnd)
		ind.render()
		p.tray.SetTip(appTitle + ": " + tech + " is connected")
	})
	return ind
}

func (i *winIndicator) render() {
	i.mu.Lock()
	text := fmt.Sprintf("● %s is %s · %s", i.tech, i.status, strings.Join(i.perms, ", "))
	i.mu.Unlock()
	winapi.SetText(i.label, text)
}

var amber uintptr

func amberBrush() uintptr {
	if amber == 0 {
		amber = winapi.SolidBrush(0x0000C8FF) // amber, 0x00BBGGRR
	}
	return amber
}

func (i *winIndicator) proc(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case winapi.WM_CTLCOLORSTATIC:
		winapi.TransparentText(wp, 0x00000000)
		return amberBrush(), true
	case winapi.WM_COMMAND:
		switch wp & 0xffff {
		case idEnd:
			if i.hooks.End != nil {
				go i.hooks.End()
			}
		case idPerms:
			m := winapi.CreatePopupMenu()
			i.mu.Lock()
			cur := append([]string(nil), i.perms...)
			i.mu.Unlock()
			has := map[string]bool{}
			for _, p := range cur {
				has[p] = true
			}
			opts := []string{"control", "clipboard", "transfer"}
			for n, k := range opts {
				flags := uint32(winapi.MF_STRING)
				if has[k] {
					flags |= winapi.MF_CHECKED
				} else {
					flags |= winapi.MF_GRAYED // once withdrawn, a permission needs a new session
				}
				winapi.AppendMenu(m, flags, uintptr(idPermC+n), permLabels[k])
			}
			r := winapi.WindowRect(h)
			sel := winapi.TrackPopup(m, h, r.Left+428, r.Bottom)
			if sel >= idPermC && int(sel-idPermC) < len(opts) {
				k := opts[sel-idPermC]
				next := []string{}
				for _, p := range cur {
					if p != k {
						next = append(next, p)
					}
				}
				i.SetPerms(next)
				if i.hooks.ChangePerms != nil {
					go i.hooks.ChangePerms(next)
				}
			}
		}
		return 0, true
	case winapi.WM_CLOSE:
		return 0, true // the indicator cannot be closed while the session runs
	}
	return 0, false
}

func (i *winIndicator) SetPerms(p []string) {
	i.mu.Lock()
	i.perms = p
	i.mu.Unlock()
	i.p.ui.Post(i.render)
}

func (i *winIndicator) SetStatus(s string) {
	i.mu.Lock()
	switch s {
	case "Connected":
		i.status = "connected"
	default:
		i.status = strings.ToLower(s)
	}
	i.mu.Unlock()
	i.p.ui.Post(i.render)
}

func (i *winIndicator) Close() {
	i.p.ui.Post(func() {
		if winapi.IsWindow(i.hwnd) {
			winapi.DestroyWindow(i.hwnd)
		}
		i.p.tray.SetTip(appTitle + " agent: waiting for support requests")
	})
}

func messageBox(text string, warn bool) {
	f := uint32(winapi.MB_ICONINFORMATION | winapi.MB_TOPMOST)
	if warn {
		f = winapi.MB_ICONWARNING | winapi.MB_TOPMOST
	}
	winapi.MessageBox(0, text, appTitle, f)
}

func confirm(text string) bool {
	return winapi.MessageBox(0, text, appTitle, winapi.MB_YESNO|winapi.MB_ICONQUESTION|winapi.MB_TOPMOST) == winapi.IDYES
}
