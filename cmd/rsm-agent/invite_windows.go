//go:build windows

package main

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const (
	idInvOK     = 301
	idInvCancel = 302
	idInvCode   = 303
	idInvAuth   = 304
	idInvPerm   = 310
)

// dialog runs a small modal-style window on the UI thread and blocks the
// caller until it closes. build adds the controls; ok returns false to keep
// the window open (for example to show a validation message).
func (p *winPlatform) dialog(title string, w, h int32, build func(hwnd windows.HWND), ok func(hwnd windows.HWND) bool) bool {
	result := make(chan bool, 1)
	p.ui.Do(func() {
		winapi.RegisterClass("RSMInvite", uintptr(winapi.COLOR_BTNFACE+1))
		var hwnd windows.HWND
		finish := func(v bool) {
			select {
			case result <- v:
			default:
			}
			winapi.UnregisterDialog(hwnd)
			winapi.DestroyWindow(hwnd)
		}
		sw, sh := winapi.SystemMetric(winapi.SM_CXSCREEN), winapi.SystemMetric(winapi.SM_CYSCREEN)
		hwnd = winapi.CreateWindow(winapi.WS_EX_TOPMOST|winapi.WS_EX_DLGMODALFRAME, "RSMInvite", title,
			winapi.WS_CAPTION|winapi.WS_SYSMENU|winapi.WS_VISIBLE, (sw-w)/2, (sh-h)/2, w, h, 0, 0,
			func(hh windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
				switch msg {
				case winapi.WM_COMMAND:
					switch wp & 0xffff {
					case idInvOK:
						if ok(hh) {
							finish(true)
						}
					case idInvCancel, winapi.IDCANCEL:
						finish(false)
					}
					return 0, true
				case winapi.WM_CLOSE:
					finish(false)
					return 0, true
				}
				return 0, false
			})
		build(hwnd)
		winapi.RegisterDialog(hwnd)
		winapi.SetForeground(hwnd)
	})
	return <-result
}

// askCode asks for the 6-digit code the technician reads out (generic
// one-time client without an invitation in client.json).
func askCode(ap agentcore.Platform, note string) (string, bool) {
	p := ap.(*winPlatform)
	var edit windows.HWND
	var code string
	ok := p.dialog("Remote support", 440, 250, func(hwnd windows.HWND) {
		t := winapi.Control(hwnd, "STATIC", "Join a remote support session", 0, 20, 16, 400, 26, 0)
		winapi.SetFont(t, winapi.BoldFont(18))
		winapi.Control(hwnd, "STATIC", strings.TrimSpace(note), 0, 20, 46, 400, 44, 0)
		winapi.Control(hwnd, "STATIC", "Type the 6-digit code your technician gives you:", 0, 20, 96, 400, 20, 0)
		edit = winapi.ControlEx(hwnd, winapi.WS_EX_CLIENTEDGE, "EDIT", "", winapi.ES_NUMBER|winapi.WS_TABSTOP, 20, 120, 140, 30, idInvCode)
		winapi.SetFont(edit, winapi.BoldFont(20))
		winapi.SendMessage(edit, winapi.EM_SETLIMITTEXT, 6, 0)
		winapi.Control(hwnd, "BUTTON", "Continue", winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, 220, 165, 95, 30, idInvOK)
		winapi.Control(hwnd, "BUTTON", "Cancel", winapi.WS_TABSTOP, 325, 165, 90, 30, idInvCancel)
		winapi.SetFocus(edit)
	}, func(hwnd windows.HWND) bool {
		code = strings.ReplaceAll(strings.TrimSpace(winapi.GetText(edit)), " ", "")
		if len(code) != 6 {
			winapi.MessageBox(hwnd, "Enter the 6-digit code your technician reads to you.", appTitle, winapi.MB_ICONWARNING)
			return false
		}
		return true
	})
	return code, ok
}

func kindWording(info *agentcore.InviteInfo) (headline, body string) {
	exp := time.Unix(info.ExpiresAt, 0).Format("Mon 2 Jan 15:04")
	switch info.Kind {
	case protocol.KindOneTime:
		return info.Technician + " wants to help you now",
			"This is a one-time session. Nothing is installed: the program closes when the session ends, and the technician cannot connect again without a new code."
	case protocol.KindSupport:
		return info.Technician + " wants to set up remote support",
			"The support program is installed for your user account so your technician can reconnect without asking each time, for as long as this support case stays open. You always see a bar at the top of the screen while they are connected. When they close the support case it removes itself; you can also remove it from Settings > Apps."
	case protocol.KindAccess:
		return "Allow unattended access to this PC?",
			"An administrator of " + info.Org + " can connect at any time, including when nobody is at the PC, until the device is removed from their console. A tray notice and a bar at the top of the screen are always shown. Windows will ask for administrator approval. Only continue if your organisation asked you to."
	default:
		return info.Technician + " wants to enable remote support",
			"Each future session will ask you to accept and type a new code (invitation valid until " + exp + ")."
	}
}

// askInvite shows who is asking, how the client will run and what the
// technician may do. For one-time and support sessions the person can
// untick anything except viewing the screen.
func askInvite(ap agentcore.Platform, info *agentcore.InviteInfo) ([]string, bool) {
	p := ap.(*winPlatform)
	checks := map[string]windows.HWND{}
	var auth windows.HWND
	head, body := kindWording(info)
	fixed := info.Kind == protocol.KindAccess || info.Kind == protocol.KindAttended
	var granted []string
	h := int32(250 + 24*len(info.Perms))
	if info.Kind == protocol.KindAccess {
		h += 30
	}
	ok := p.dialog("Remote support", 540, h+70, func(hwnd windows.HWND) {
		t := winapi.Control(hwnd, "STATIC", head, 0, 20, 16, 500, 26, 0)
		winapi.SetFont(t, winapi.BoldFont(18))
		from := info.Technician
		if info.TechEmail != "" {
			from += " · " + info.TechEmail
		}
		winapi.Control(hwnd, "STATIC", fmt.Sprintf("%s · %s · %q", from, info.Org, info.Label), 0, 20, 44, 500, 20, 0)
		winapi.Control(hwnd, "STATIC", body, 0, 20, 70, 500, 76, 0)
		y := int32(152)
		ask := "They will be able to:"
		if !fixed {
			ask = "They are asking to (untick what you do not want):"
		}
		winapi.Control(hwnd, "STATIC", ask, 0, 20, y, 500, 20, 0)
		y += 24
		for i, k := range info.Perms {
			c := winapi.Control(hwnd, "BUTTON", permLabels[k], winapi.BS_AUTOCHECKBOX|winapi.WS_TABSTOP, 36, y, 460, 22, idInvPerm+i)
			winapi.SetChecked(c, true)
			if k == "view" || fixed {
				winapi.Enable(c, false)
			}
			checks[k] = c
			y += 24
		}
		if info.Kind == protocol.KindAccess {
			y += 6
			auth = winapi.Control(hwnd, "BUTTON", "I am allowed to approve unattended access to this PC", winapi.BS_AUTOCHECKBOX|winapi.WS_TABSTOP, 20, y, 480, 22, idInvAuth)
			y += 28
		}
		label := "Allow"
		switch info.Kind {
		case protocol.KindSupport:
			label = "Allow and install"
		case protocol.KindAccess:
			label = "Install…"
		}
		winapi.Control(hwnd, "BUTTON", label, winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, 540-260, y+20, 130, 30, idInvOK)
		winapi.Control(hwnd, "BUTTON", "Decline", winapi.WS_TABSTOP, 540-120, y+20, 90, 30, idInvCancel)
	}, func(hwnd windows.HWND) bool {
		if auth != 0 && !winapi.Checked(auth) {
			winapi.MessageBox(hwnd, "Tick the box to confirm that you are allowed to approve unattended access, or choose Decline.", appTitle, winapi.MB_ICONWARNING)
			return false
		}
		granted = granted[:0]
		for _, k := range info.Perms {
			if c := checks[k]; c != 0 && winapi.Checked(c) {
				granted = append(granted, k)
			}
		}
		return true
	})
	return granted, ok
}
