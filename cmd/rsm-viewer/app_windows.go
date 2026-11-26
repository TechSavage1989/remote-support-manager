//go:build windows

package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/keys"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/screen"
	"github.com/TechSavage1989/remote-support-manager/internal/transfer"
	"github.com/TechSavage1989/remote-support-manager/internal/viewercore"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const title = "Remote Support Manager Viewer"

// cwDefault is CW_USEDEFAULT as a signed coordinate.
const cwDefault int32 = -0x80000000

// Menu command ids.
const (
	cmdEnd = 100 + iota
	cmdExit
	cmdFit
	cmdActual
	cmdFull
	cmdCapture
	cmdReleaseKeys
	cmdClipSync
	cmdClipBoth
	cmdClipToRemote
	cmdClipToLocal
	cmdPasteKeys
	cmdFiles
	cmdCheckUpdates
	cmdAbout
	cmdRefresh
	cmdComboBase = 300 // + index in protocol.Combos
	cmdMonBase   = 400 // + monitor index
)

type app struct {
	ui     *winapi.UI
	set    *settings
	main   windows.HWND
	status windows.HWND
	scr    windows.HWND
	menu   uintptr
	mMon   uintptr

	c *viewercore.Client

	mu        sync.Mutex
	bgra      []byte
	fw, fh    int
	fit       bool
	full      bool
	saved     winapi.RECT
	savedSt   uintptr
	capture   bool
	hook      winapi.Hook
	focused   bool
	esc       keys.Escape
	ctrlDown  bool
	altDown   bool
	sx, sy    int32
	lastMove  time.Time
	stateText string
	clipOn    bool
	clipDir   string
	files     *fileManager
	remoteDL  string
	ended     bool
	upd       *updateUI
}

var theApp *app

func run() {
	winapi.EnableDPIAwareness()
	winapi.InitCommonControls()
	a := &app{set: loadSettings(), fit: true, capture: true, clipDir: clipsync.Both}
	theApp = a
	link := ""
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(strings.ToLower(arg), "rsm:") {
			link = arg
		}
		if arg == "--register" {
			registerProtocol(true)
			return
		}
		if arg == "--unregister" {
			winapi.UnregisterURLProtocol("rsm")
			return
		}
	}
	done := make(chan struct{})
	a.ui = winapi.NewUI(func(ui *winapi.UI) {
		a.create(func() { close(done) })
	})
	if !a.set.AskedProtocol {
		a.set.AskedProtocol = true
		a.set.save()
		a.ui.Do(func() {
			if winapi.MessageBox(a.main, "Open \"Open in viewer\" links from the Remote Support Manager console with this app?\n\nThis is registered for your Windows account only.", title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) == winapi.IDYES {
				registerProtocol(false)
			}
		})
	}
	a.startUpdates()
	if link == "" {
		a.ui.Post(func() { a.askForLink() })
	} else {
		go a.connect(link)
	}
	<-done
}

func registerProtocol(announce bool) {
	exe, _ := os.Executable()
	err := winapi.RegisterURLProtocol("rsm", exe, "Remote Support Manager viewer link")
	if announce {
		msg := "rsm:// links now open in this viewer for your Windows account."
		if err != nil {
			msg = "Could not register rsm:// links: " + err.Error()
		}
		winapi.MessageBox(0, msg, title, winapi.MB_ICONINFORMATION)
	}
}

func (a *app) create(onClose func()) {
	winapi.RegisterClass("RSMViewer", uintptr(winapi.COLOR_BTNFACE+1))
	winapi.RegisterClass("RSMScreen", winapi.StockObject(4 /* BLACK_BRUSH */))
	a.main = winapi.CreateWindow(0, "RSMViewer", title, winapi.WS_OVERLAPPEDWINDOW|winapi.WS_CLIPCHILDREN|winapi.WS_VISIBLE,
		cwDefault, cwDefault, 1280, 800, 0, 0,
		func(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
			return a.mainProc(h, msg, wp, lp, onClose)
		})
	a.status = winapi.Control(a.main, "STATIC", "Starting…", winapi.SS_LEFT, 8, 4, 1200, 20, 0)
	a.scr = winapi.CreateWindow(0, "RSMScreen", "", winapi.WS_CHILD|winapi.WS_VISIBLE|winapi.WS_TABSTOP|winapi.WS_CLIPSIBLINGS, 0, 28, 1200, 700, a.main, 0, a.screenProc)
	a.buildMenu()
	winapi.ClipboardListener(a.main, true)
	a.layout()
}

func (a *app) buildMenu() {
	m := winapi.CreateMenu()
	sess := winapi.CreatePopupMenu()
	winapi.AppendMenu(sess, winapi.MF_STRING, cmdFiles, "&Files…")
	winapi.AppendMenu(sess, winapi.MF_STRING, cmdRefresh, "&Refresh screen")
	winapi.AppendMenu(sess, winapi.MF_SEPARATOR, 0, "")
	winapi.AppendMenu(sess, winapi.MF_STRING, cmdEnd, "&End session")
	winapi.AppendMenu(sess, winapi.MF_STRING, cmdExit, "E&xit")
	winapi.AppendMenu(m, winapi.MF_POPUP, sess, "&Session")

	view := winapi.CreatePopupMenu()
	winapi.AppendMenu(view, winapi.MF_STRING, cmdFit, "&Fit to window")
	winapi.AppendMenu(view, winapi.MF_STRING, cmdActual, "&Actual size")
	winapi.AppendMenu(view, winapi.MF_STRING, cmdFull, "F&ull screen\tCtrl+Alt+F11")
	a.mMon = winapi.CreatePopupMenu()
	winapi.AppendMenu(a.mMon, winapi.MF_STRING|winapi.MF_GRAYED, cmdMonBase, "Waiting for the remote PC…")
	winapi.AppendMenu(view, winapi.MF_POPUP, a.mMon, "&Monitor")
	winapi.AppendMenu(m, winapi.MF_POPUP, view, "&View")

	kb := winapi.CreatePopupMenu()
	winapi.AppendMenu(kb, winapi.MF_STRING, cmdCapture, "Send &shortcuts to the remote PC (Alt+Tab, Windows key…)")
	winapi.AppendMenu(kb, winapi.MF_STRING, cmdReleaseKeys, "&Release held keys\tCtrl+Alt+F12 stops sending shortcuts")
	winapi.AppendMenu(kb, winapi.MF_SEPARATOR, 0, "")
	labels := map[string]string{"ctrl-alt-del": "Ctrl+Alt+&Del", "ctrl-shift-esc": "Ctrl+Shift+Esc (Task Manager)", "alt-tab": "Alt+Tab", "win": "Windows key",
		"win-r": "Windows+R (Run)", "win-l": "Windows+L (Lock)", "alt-f4": "Alt+F4", "print-screen": "Print Screen"}
	for i, c := range protocol.Combos {
		winapi.AppendMenu(kb, winapi.MF_STRING, uintptr(cmdComboBase+i), "Send "+labels[c])
	}
	winapi.AppendMenu(m, winapi.MF_POPUP, kb, "&Keyboard")

	cl := winapi.CreatePopupMenu()
	winapi.AppendMenu(cl, winapi.MF_STRING, cmdClipSync, "&Sync clipboard")
	winapi.AppendMenu(cl, winapi.MF_SEPARATOR, 0, "")
	winapi.AppendMenu(cl, winapi.MF_STRING, cmdClipBoth, "&Both directions")
	winapi.AppendMenu(cl, winapi.MF_STRING, cmdClipToRemote, "&This PC → remote only")
	winapi.AppendMenu(cl, winapi.MF_STRING, cmdClipToLocal, "&Remote → this PC only")
	winapi.AppendMenu(cl, winapi.MF_SEPARATOR, 0, "")
	winapi.AppendMenu(cl, winapi.MF_STRING, cmdPasteKeys, "&Paste as keystrokes…")
	winapi.AppendMenu(m, winapi.MF_POPUP, cl, "&Clipboard")

	help := winapi.CreatePopupMenu()
	winapi.AppendMenu(help, winapi.MF_STRING, cmdCheckUpdates, "App &updates…")
	winapi.AppendMenu(help, winapi.MF_STRING, cmdAbout, "&About")
	winapi.AppendMenu(m, winapi.MF_POPUP, help, "&Help")
	winapi.SetMenu(a.main, m)
	a.menu = m
	a.syncMenu()
}

func (a *app) syncMenu() {
	winapi.CheckMenu(a.menu, cmdFit, a.fit)
	winapi.CheckMenu(a.menu, cmdActual, !a.fit)
	winapi.CheckMenu(a.menu, cmdFull, a.full)
	winapi.CheckMenu(a.menu, cmdCapture, a.capture)
	winapi.CheckMenu(a.menu, cmdClipSync, a.clipOn)
	winapi.CheckMenu(a.menu, cmdClipBoth, a.clipDir == clipsync.Both)
	winapi.CheckMenu(a.menu, cmdClipToRemote, a.clipDir == clipsync.ToRemote)
	winapi.CheckMenu(a.menu, cmdClipToLocal, a.clipDir == clipsync.ToLocal)
	has := func(p protocol.Permission) bool { return a.c != nil && a.c.Has(p) }
	for i := range protocol.Combos {
		winapi.EnableMenu(a.menu, uintptr(cmdComboBase+i), has(protocol.PermControl))
	}
	winapi.EnableMenu(a.menu, cmdPasteKeys, has(protocol.PermControl))
	winapi.EnableMenu(a.menu, cmdClipSync, has(protocol.PermClipboard))
	winapi.EnableMenu(a.menu, cmdFiles, has(protocol.PermTransfer))
	winapi.EnableMenu(a.menu, cmdEnd, a.c != nil && !a.ended)
	winapi.DrawMenuBar(a.main)
}

func (a *app) layout() {
	r := winapi.ClientRect(a.main)
	top := int32(28)
	if a.full {
		top = 0
		winapi.ShowWindow(a.status, winapi.SW_HIDE)
	} else {
		winapi.ShowWindow(a.status, winapi.SW_SHOW)
		winapi.MoveWindow(a.status, 8, 5, r.Right-16, 20)
	}
	winapi.MoveWindow(a.scr, 0, top, r.Right, r.Bottom-top)
	if a.upd != nil {
		a.upd.relayout()
	}
	a.updateScroll()
	winapi.Invalidate(a.scr)
}

func (a *app) setStatus(s string) {
	a.stateText = s
	a.refreshStatus()
}

func (a *app) refreshStatus() {
	s := a.stateText
	if a.c != nil && a.c.State() == "active" {
		kb := "Keyboard: normal (Alt+Tab and Windows key stay on this PC)"
		if a.capture {
			kb = "Keyboard: shortcuts go to the remote PC · Ctrl+Alt+F12 to stop"
		}
		s += " · " + kb
	}
	winapi.SetText(a.status, s)
}

func (a *app) askForLink() {
	a.setStatus("Open a session from the console with \"Open in viewer\", or paste a viewer link.")
	promptText(a.main, "Paste the viewer link from the console (starts with rsm://connect):", "", func(link string) {
		if link != "" {
			go a.connect(link)
		}
	})
}

// ------------------------------------------------------------ session

func (a *app) connect(link string) {
	l, err := viewercore.ParseLink(link)
	if err != nil {
		a.ui.Post(func() {
			winapi.MessageBox(a.main, "That is not a valid viewer link: "+err.Error(), title, winapi.MB_ICONERROR)
		})
		return
	}
	a.ui.Post(func() { a.setStatus("Connecting to " + l.Server + "…") })
	c, err := viewercore.Redeem(l)
	if err != nil {
		a.ui.Post(func() {
			a.setStatus(err.Error())
			winapi.MessageBox(a.main, err.Error()+"\n\nStart a new session from the console.", title, winapi.MB_ICONERROR)
		})
		return
	}
	device, _ := c.Info["device"].(string)
	code, _ := c.Info["code"].(string)
	c.CB = viewercore.Callbacks{
		State: func(st, scode, reason string) {
			a.ui.Post(func() {
				switch st {
				case "pending":
					if scode == "" {
						scode = code
					}
					a.setStatus(fmt.Sprintf("Waiting for the person at %s to accept. Read them this support code: %s", device, scode))
				case "joining":
					a.setStatus("Accepted. Connecting to " + device + "…")
				case "active":
					a.setStatus(fmt.Sprintf("Connected to %s · Permissions: %s", device, strings.Join(c.Perms(), ", ")))
				case "suspended":
					a.setStatus("Connection interrupted. Reconnecting… (held keys were released)")
				case "ended":
					a.setStatus("Session ended: " + reason)
				}
				a.syncMenu()
			})
		},
		Hello: func(h protocol.Hello, resumed bool) {
			a.ui.Post(func() {
				winapi.SetText(a.main, h.Device+" · "+title)
				a.syncMenu()
				if a.files != nil {
					a.files.permsChanged()
				}
			})
			if c.Has(protocol.PermTransfer) {
				_ = c.ListRemote("") // learn the remote Downloads folder
			}
		},
		Perms: func(p []string) {
			a.ui.Post(func() {
				a.setStatus(fmt.Sprintf("Connected to %s · The person at the PC changed permissions to: %s", device, strings.Join(p, ", ")))
				if !c.Has(protocol.PermClipboard) {
					a.clipOn = false
				}
				a.syncMenu()
				if a.files != nil {
					a.files.permsChanged()
				}
			})
		},
		Monitors: func(m protocol.Monitors) { a.ui.Post(func() { a.rebuildMonitors(m) }) },
		Frame:    func(d image.Rectangle) { a.onFrame(d) },
		Denied: func(d protocol.Denied) {
			a.ui.Post(func() { a.setStatus("Not allowed in this session: " + d.Blocked + " needs " + d.Need + " permission") })
		},
		Notice: func(n protocol.Notice) {
			a.ui.Post(func() { winapi.MessageBox(a.main, n.Text, title, winapi.MB_ICONINFORMATION) })
		},
		Listing: func(l viewercore.Listing) {
			if l.Path == "" {
				for _, e := range l.Entries {
					if strings.HasSuffix(strings.ToLower(e.Name), `\downloads`) {
						a.mu.Lock()
						a.remoteDL = e.Name
						a.mu.Unlock()
					}
				}
			}
			a.ui.Post(func() {
				if a.files != nil {
					a.files.remoteListing(l)
				}
			})
		},
		Ended: func(reason string) {
			a.ui.Post(func() {
				a.ended = true
				a.unhook()
				a.syncMenu()
				winapi.MessageBox(a.main, "The session has ended: "+reason, title, winapi.MB_ICONINFORMATION)
			})
		},
		Transfer: func(p transfer.Progress) {
			a.ui.Post(func() {
				a.ensureFiles()
				a.files.progress(p)
			})
		},
		Conflict: func(ci transfer.ConflictInfo, reply func(string, bool)) {
			a.ui.Post(func() { conflictDialog(a.main, ci, reply) })
		},
		RemoteFilesCopied: func(paths []string) {
			a.ui.Post(func() { a.offerRemoteFiles(paths) })
		},
		ApplyClipboard: func(cl clipsync.Clip) error {
			var err error
			a.ui.Do(func() { err = winapi.WriteClipboard(a.main, cl) })
			return err
		},
	}
	a.mu.Lock()
	a.c = c
	a.mu.Unlock()
	a.ui.Post(func() {
		a.set.LastServer, a.set.LastPin = l.Server, l.Pin
		a.set.save()
	})
	a.ui.Post(func() {
		a.setStatus(fmt.Sprintf("Waiting for the person at %s to accept. Read them this support code: %s", device, code))
	})
	c.Run(context.Background())
}

func (a *app) rebuildMonitors(m protocol.Monitors) {
	winapi.ClearMenu(a.mMon)
	for _, mon := range m.Monitors {
		label := fmt.Sprintf("Monitor %d (%d×%d)", mon.Index+1, mon.W, mon.H)
		if mon.Primary {
			label += " · primary"
		}
		winapi.AppendMenu(a.mMon, winapi.MF_STRING, uintptr(cmdMonBase+mon.Index), label)
		winapi.CheckMenu(a.mMon, uintptr(cmdMonBase+mon.Index), mon.Index == m.Current)
	}
	winapi.DrawMenuBar(a.main)
}

func (a *app) onFrame(d image.Rectangle) {
	a.c.Canvas(func(img *image.RGBA) {
		if img == nil {
			return
		}
		a.mu.Lock()
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		if a.fw != w || a.fh != h || len(a.bgra) != len(img.Pix) {
			a.fw, a.fh = w, h
			a.bgra = winapi.RGBAtoBGRA(img.Pix, nil)
		} else {
			for y := d.Min.Y; y < d.Max.Y; y++ {
				o := y*img.Stride + d.Min.X*4
				n := d.Dx() * 4
				winapi.RGBAtoBGRA(img.Pix[o:o+n], a.bgra[o:o+n])
			}
		}
		a.mu.Unlock()
	})
	a.ui.Post(func() {
		a.updateScroll()
		winapi.Invalidate(a.scr)
	})
}

func (a *app) shownRect() image.Rectangle {
	r := winapi.ClientRect(a.scr)
	if a.fit {
		return screen.Fit(a.fw, a.fh, int(r.Right), int(r.Bottom))
	}
	return image.Rect(-int(a.sx), -int(a.sy), a.fw-int(a.sx), a.fh-int(a.sy))
}

func (a *app) updateScroll() {
	r := winapi.ClientRect(a.scr)
	if a.fit || a.fw == 0 {
		a.sx, a.sy = 0, 0
		winapi.SetScroll(a.scr, winapi.SB_HORZ, 0, 0, 0)
		winapi.SetScroll(a.scr, winapi.SB_VERT, 0, 0, 0)
		return
	}
	a.sx = clamp32(a.sx, 0, int32(a.fw)-r.Right)
	a.sy = clamp32(a.sy, 0, int32(a.fh)-r.Bottom)
	winapi.SetScroll(a.scr, winapi.SB_HORZ, int32(a.fw), r.Right, a.sx)
	winapi.SetScroll(a.scr, winapi.SB_VERT, int32(a.fh), r.Bottom, a.sy)
}

func clamp32(v, lo, hi int32) int32 {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ------------------------------------------------------------ main window

func (a *app) mainProc(h windows.HWND, msg uint32, wp, lp uintptr, onClose func()) (uintptr, bool) {
	switch msg {
	case winapi.WM_SIZE:
		a.layout()
		return 0, true
	case winapi.WM_ACTIVATE:
		if wp&0xffff == winapi.WA_INACTIVE {
			a.releaseAll()
		} else if a.upd != nil {
			a.upd.flow.FocusReturned()
		}
		return 0, false
	case winapi.WM_COMMAND:
		a.command(wp & 0xffff)
		return 0, true
	case winapi.WM_CLIPBOARDUPDATE:
		a.localClipboardChanged()
		return 0, true
	case winapi.WM_CLOSE:
		if a.c != nil && !a.ended {
			if winapi.MessageBox(h, "End the remote support session and close the viewer?", title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) != winapi.IDYES {
				return 0, true
			}
			a.c.End()
		}
		if a.upd != nil && a.upd.busy() {
			if winapi.MessageBox(h, "An update is downloading. Close anyway? The download will be discarded.", title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) != winapi.IDYES {
				return 0, true
			}
		}
		a.unhook()
		winapi.ClipboardListener(a.main, false)
		winapi.DestroyWindow(h)
		return 0, true
	case winapi.WM_DESTROY:
		winapi.PostQuitMessage(0)
		onClose()
		return 0, true
	}
	return 0, false
}

func (a *app) command(id uintptr) {
	c := a.c
	switch {
	case id == cmdEnd:
		if c != nil && winapi.MessageBox(a.main, "End the session for both sides?", title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) == winapi.IDYES {
			go c.End()
		}
	case id == cmdExit:
		winapi.PostMessage(a.main, winapi.WM_CLOSE, 0, 0)
	case id == cmdFit:
		a.fit = true
		a.layout()
	case id == cmdActual:
		a.fit = false
		a.layout()
	case id == cmdFull:
		a.toggleFull()
	case id == cmdCapture:
		a.capture = !a.capture
		if !a.capture {
			a.unhook()
		} else if a.focused {
			a.installHook()
		}
		a.refreshStatus()
	case id == cmdReleaseKeys:
		a.releaseAll()
	case id == cmdRefresh && c != nil:
		go func() { _ = c.Send(map[string]string{"t": protocol.TScrRefresh}, nil) }()
	case id == cmdClipSync && c != nil:
		a.clipOn = !a.clipOn
		on, dir := a.clipOn, a.clipDir
		go func() {
			if err := c.SetClipboardSync(on, dir); err != nil {
				a.ui.Post(func() { a.clipOn = false; a.syncMenu(); a.setStatus(err.Error()) })
			}
		}()
	case id == cmdClipBoth || id == cmdClipToRemote || id == cmdClipToLocal:
		a.clipDir = map[uintptr]string{cmdClipBoth: clipsync.Both, cmdClipToRemote: clipsync.ToRemote, cmdClipToLocal: clipsync.ToLocal}[id]
		if c != nil && a.clipOn {
			on, dir := a.clipOn, a.clipDir
			go func() { _ = c.SetClipboardSync(on, dir) }()
		}
	case id == cmdPasteKeys && c != nil:
		cl, err := winapi.ReadClipboard(a.main)
		if err != nil || cl.Text == "" {
			winapi.MessageBox(a.main, "Copy some text on this PC first.", title, winapi.MB_ICONINFORMATION)
			break
		}
		n := len([]rune(cl.Text))
		if winapi.MessageBox(a.main, fmt.Sprintf("Type %d characters from your clipboard on the remote PC?\n\nThey go to whichever window is focused there.", n), title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) == winapi.IDYES {
			text := cl.Text
			go func() {
				if err := c.PasteAsKeystrokes(text); err != nil {
					a.ui.Post(func() { a.setStatus(err.Error()) })
				}
			}()
		}
	case id == cmdFiles:
		a.ensureFiles()
		a.files.show()
	case id == cmdCheckUpdates:
		a.upd.checkNow(true)
	case id == cmdAbout:
		winapi.MessageBox(a.main, "Remote Support Manager viewer "+Version+"\n\nLocal shortcuts:\nCtrl+Alt+F12  stop sending shortcuts to the remote PC\nCtrl+Alt+F11  full screen on/off\n\nCtrl+Alt+Del cannot be captured from the keyboard; use the Keyboard menu.", title, winapi.MB_ICONINFORMATION)
	case id >= cmdComboBase && id < cmdComboBase+uintptr(len(protocol.Combos)) && c != nil:
		name := protocol.Combos[id-cmdComboBase]
		go func() {
			if err := c.Combo(name); err != nil {
				a.ui.Post(func() { a.setStatus(err.Error()) })
			}
		}()
	case id >= cmdMonBase && id < cmdMonBase+32 && c != nil:
		idx := int(id - cmdMonBase)
		go func() { _ = c.SelectMonitor(idx) }()
	}
	a.syncMenu()
}

func (a *app) toggleFull() {
	if !a.full {
		a.saved = winapi.WindowRect(a.main)
		a.savedSt = winapi.Style(a.main)
		winapi.SetMenu(a.main, 0)
		winapi.SetStyle(a.main, (a.savedSt&^uintptr(winapi.WS_OVERLAPPEDWINDOW))|uintptr(winapi.WS_POPUP)|uintptr(winapi.WS_VISIBLE))
		a.full = true
		winapi.ShowWindow(a.main, winapi.SW_MAXIMIZE)
	} else {
		a.full = false
		winapi.SetStyle(a.main, a.savedSt)
		winapi.SetMenu(a.main, a.menu)
		winapi.ShowWindow(a.main, winapi.SW_RESTORE)
		r := a.saved
		winapi.SetWindowPos(a.main, 0, r.Left, r.Top, r.Right-r.Left, r.Bottom-r.Top, winapi.SWP_NOZORDER|winapi.SWP_FRAMECHANGED)
	}
	a.layout()
	winapi.SetFocus(a.scr)
}

// ------------------------------------------------------------ screen window

func (a *app) screenProc(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case winapi.WM_ERASEBKGND:
		return 1, true
	case winapi.WM_PAINT:
		var ps winapi.PAINTSTRUCT
		dc := winapi.BeginPaint(h, &ps)
		r := winapi.ClientRect(h)
		a.mu.Lock()
		shown := a.shownRect()
		if a.fw > 0 {
			winapi.DrawBGRA(dc, int32(shown.Min.X), int32(shown.Min.Y), int32(shown.Dx()), int32(shown.Dy()), a.bgra, int32(a.fw), int32(a.fh))
		}
		a.mu.Unlock()
		black := winapi.StockObject(4)
		// Fill margins around the picture.
		for _, m := range []winapi.RECT{
			{Left: 0, Top: 0, Right: r.Right, Bottom: int32(shown.Min.Y)},
			{Left: 0, Top: int32(shown.Max.Y), Right: r.Right, Bottom: r.Bottom},
			{Left: 0, Top: int32(shown.Min.Y), Right: int32(shown.Min.X), Bottom: int32(shown.Max.Y)},
			{Left: int32(shown.Max.X), Top: int32(shown.Min.Y), Right: r.Right, Bottom: int32(shown.Max.Y)},
		} {
			if a.fw == 0 {
				m = r
			}
			if m.Right > m.Left && m.Bottom > m.Top {
				winapi.FillRect(dc, &m, black)
			}
		}
		winapi.EndPaint(h, &ps)
		return 0, true
	case winapi.WM_HSCROLL, winapi.WM_VSCROLL:
		bar := winapi.SB_HORZ
		if msg == winapi.WM_VSCROLL {
			bar = winapi.SB_VERT
		}
		si := winapi.GetScroll(h, bar)
		pos := si.Pos
		switch wp & 0xffff {
		case winapi.SB_LINEUP:
			pos -= 40
		case winapi.SB_LINEDOWN:
			pos += 40
		case winapi.SB_PAGEUP:
			pos -= int32(si.Page)
		case winapi.SB_PAGEDOWN:
			pos += int32(si.Page)
		case winapi.SB_THUMBTRACK, winapi.SB_THUMBPOSITION:
			pos = si.TrackPos
		}
		if bar == winapi.SB_HORZ {
			a.sx = pos
		} else {
			a.sy = pos
		}
		a.updateScroll()
		winapi.Invalidate(h)
		return 0, true
	case winapi.WM_SETFOCUS:
		a.focused = true
		if a.capture {
			a.installHook()
		}
		return 0, true
	case winapi.WM_KILLFOCUS:
		a.focused = false
		a.unhook()
		a.releaseAll()
		return 0, true
	case winapi.WM_MOUSEMOVE, winapi.WM_LBUTTONDOWN, winapi.WM_LBUTTONUP, winapi.WM_RBUTTONDOWN, winapi.WM_RBUTTONUP,
		winapi.WM_MBUTTONDOWN, winapi.WM_MBUTTONUP, winapi.WM_LBUTTONDBLCLK, winapi.WM_RBUTTONDBLCLK, winapi.WM_MOUSEWHEEL, winapi.WM_MOUSEHWHEEL:
		a.mouse(h, msg, wp, lp)
		return 0, true
	case winapi.WM_KEYDOWN, winapi.WM_KEYUP, winapi.WM_SYSKEYDOWN, winapi.WM_SYSKEYUP:
		// Reached only when the low-level hook is off (normal keyboard mode).
		k := protocol.Key{Scan: uint16((lp >> 16) & 0xff), Ext: (lp>>24)&1 == 1, VK: uint16(wp), Down: msg == winapi.WM_KEYDOWN || msg == winapi.WM_SYSKEYDOWN}
		if a.localShortcut(k) {
			return 0, true
		}
		a.sendKey(k)
		return 0, true
	case winapi.WM_CHAR, winapi.WM_SYSCHAR:
		return 0, true // characters come from scan codes on the remote side
	}
	return 0, false
}

func (a *app) mouse(h windows.HWND, msg uint32, wp, lp uintptr) {
	c := a.c
	if c == nil || !c.Has(protocol.PermControl) {
		if msg == winapi.WM_LBUTTONDOWN {
			winapi.SetFocus(h)
		}
		return
	}
	x, y := int(int16(lp&0xffff)), int(int16((lp>>16)&0xffff))
	if msg == winapi.WM_MOUSEWHEEL || msg == winapi.WM_MOUSEHWHEEL {
		// Wheel coordinates are screen-based; use the last known position.
		x, y = -1, -1
	}
	a.mu.Lock()
	shown := a.shownRect()
	fw, fh := a.fw, a.fh
	a.mu.Unlock()
	var rx, ry int
	var ok bool
	if x >= 0 {
		rx, ry, ok = screen.ToRemote(x, y, shown, fw, fh)
		if !ok {
			return
		}
		lastRemote = [2]int{rx, ry}
	} else {
		rx, ry = lastRemote[0], lastRemote[1]
	}
	m := protocol.Mouse{X: rx, Y: ry}
	switch msg {
	case winapi.WM_MOUSEMOVE:
		if time.Since(a.lastMove) < 12*time.Millisecond {
			return
		}
		a.lastMove = time.Now()
		m.Action = "move"
	case winapi.WM_LBUTTONDOWN, winapi.WM_LBUTTONDBLCLK:
		winapi.SetFocus(h)
		winapi.SetCapture(h)
		m.Action, m.Button = "down", "left"
	case winapi.WM_LBUTTONUP:
		winapi.ReleaseCapture()
		m.Action, m.Button = "up", "left"
	case winapi.WM_RBUTTONDOWN, winapi.WM_RBUTTONDBLCLK:
		m.Action, m.Button = "down", "right"
	case winapi.WM_RBUTTONUP:
		m.Action, m.Button = "up", "right"
	case winapi.WM_MBUTTONDOWN:
		m.Action, m.Button = "down", "middle"
	case winapi.WM_MBUTTONUP:
		m.Action, m.Button = "up", "middle"
	case winapi.WM_MOUSEWHEEL:
		m.Action, m.Delta = "wheel", int(int16(wp>>16))
	case winapi.WM_MOUSEHWHEEL:
		m.Action, m.Delta = "hwheel", int(int16(wp>>16))
	}
	go c.SendMouse(m)
}

var lastRemote [2]int

func (a *app) sendKey(k protocol.Key) {
	if c := a.c; c != nil {
		go c.SendKey(k)
	}
}

// localShortcut handles the viewer's own shortcuts. They are never sent.
func (a *app) localShortcut(k protocol.Key) bool {
	switch k.Scan {
	case keys.ScanCtrl:
		a.ctrlDown = k.Down
	case keys.ScanAlt:
		a.altDown = k.Down
	}
	ctrl, alt := a.ctrlDown || winapi.KeyDown(0x11), a.altDown || winapi.KeyDown(0x12)
	if a.esc.Check(k) {
		a.capture = false
		a.unhook()
		a.releaseAll()
		a.syncMenu()
		a.refreshStatus()
		return true
	}
	if k.Down && ctrl && alt && k.VK == 0x7A { // F11
		a.ui.Post(a.toggleFull)
		return true
	}
	return false
}

func (a *app) installHook() {
	if a.hook != 0 {
		return
	}
	a.esc.Reset()
	a.hook = winapi.InstallKeyboardHook(func(msg uintptr, kb *winapi.KBDLLHOOKSTRUCT) bool {
		if kb.Flags&0x10 != 0 { // injected by someone else: leave it
			return false
		}
		if winapi.Foreground() != a.main || !a.focused || !a.capture {
			return false
		}
		k := protocol.Key{Scan: uint16(kb.Scan), Ext: kb.Flags&1 == 1, VK: uint16(kb.VK), Down: kb.Flags&0x80 == 0}
		if a.localShortcut(k) {
			return true
		}
		a.sendKey(k)
		return true // swallow so the local PC does not act on it
	})
}

func (a *app) unhook() {
	if a.hook != 0 {
		a.hook.Unhook()
		a.hook = 0
	}
}

func (a *app) releaseAll() {
	a.esc.Reset()
	a.ctrlDown, a.altDown = false, false
	if c := a.c; c != nil {
		go c.ReleaseKeys()
	}
}

// ------------------------------------------------------------ clipboard

func (a *app) localClipboardChanged() {
	c := a.c
	if c == nil || !a.clipOn || !c.Has(protocol.PermClipboard) {
		return
	}
	cl, err := winapi.ReadClipboard(a.main)
	if err != nil {
		return
	}
	files := c.LocalClipboardChanged(cl)
	if len(files) == 0 || a.clipDir == clipsync.ToLocal {
		return
	}
	a.mu.Lock()
	dest := a.remoteDL
	a.mu.Unlock()
	if !c.Has(protocol.PermTransfer) || dest == "" {
		a.setStatus("Copied files are sent through the file transfer queue, which needs file transfer permission.")
		return
	}
	if winapi.MessageBox(a.main, fmt.Sprintf("Send the %d copied item(s) to the remote PC's Downloads folder?\n\n%s", len(files), dest), title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) == winapi.IDYES {
		a.ensureFiles()
		a.files.show()
		go func() {
			if _, err := c.Upload(files, dest); err != nil {
				a.ui.Post(func() { a.setStatus("Could not send: " + err.Error()) })
			}
		}()
	}
}

func (a *app) offerRemoteFiles(paths []string) {
	c := a.c
	if c == nil || !c.Has(protocol.PermTransfer) {
		return
	}
	dl := winapi.KnownFolder(windows.FOLDERID_Downloads)
	if winapi.MessageBox(a.main, fmt.Sprintf("The person at the remote PC copied %d item(s). Download them to %s?", len(paths), dl), title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) == winapi.IDYES {
		a.ensureFiles()
		a.files.show()
		go func() {
			if _, err := c.Download(paths, dl); err != nil {
				a.ui.Post(func() { a.setStatus("Could not download: " + err.Error()) })
			}
		}()
	}
}

func (a *app) ensureFiles() {
	if a.files == nil {
		a.files = newFileManager(a)
	}
}
