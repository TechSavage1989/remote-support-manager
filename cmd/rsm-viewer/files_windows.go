//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/transfer"
	"github.com/TechSavage1989/remote-support-manager/internal/viewercore"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const (
	idLocalList = 10 + iota
	idRemoteList
	idQueue
	idSend
	idReceive
	idLocalUp
	idRemoteUp
	idLocalPath
	idRemotePath
	idCancel
	idResume
	idRemoteRefresh
	idLocalGo
	idRemoteGo
)

type entry struct {
	name string
	dir  bool
	size int64
	up   bool // the ".." row
}

// fileManager is the two-pane transfer window. Left is this PC, right is the
// remote PC. Nothing received is ever opened or run; files land in the chosen
// folder with the Mark of the Web.
type fileManager struct {
	a                          *app
	h                          windows.HWND
	lp                         windows.HWND // local path edit
	rp                         windows.HWND
	lst                        *winapi.ListView
	rst                        *winapi.ListView
	q                          *winapi.ListView
	hint                       windows.HWND
	send, recv, cancel, resume windows.HWND

	localDir  string
	remoteDir string
	local     []entry
	remote    []entry
	rows      map[string]int
	jobs      map[string]transfer.Progress
	order     []string
}

func newFileManager(a *app) *fileManager {
	f := &fileManager{a: a, rows: map[string]int{}, jobs: map[string]transfer.Progress{}}
	f.localDir = winapi.KnownFolder(windows.FOLDERID_Downloads)
	winapi.RegisterClass("RSMFiles", uintptr(winapi.COLOR_BTNFACE+1))
	f.h = winapi.CreateWindow(winapi.WS_EX_ACCEPTFILES, "RSMFiles", "Files · "+title, winapi.WS_OVERLAPPEDWINDOW|winapi.WS_CLIPCHILDREN,
		cwDefault, cwDefault, 1100, 700, 0, 0, f.proc)
	winapi.Control(f.h, "STATIC", "This PC", winapi.SS_LEFT, 8, 8, 200, 18, 0)
	winapi.Control(f.h, "STATIC", "Remote PC", winapi.SS_LEFT, 560, 8, 200, 18, 0)
	f.lp = winapi.ControlEx(f.h, winapi.WS_EX_CLIENTEDGE, "EDIT", f.localDir, winapi.ES_AUTOHSCROLL|winapi.WS_TABSTOP, 8, 28, 400, 22, idLocalPath)
	winapi.Control(f.h, "BUTTON", "Go", winapi.WS_TABSTOP, 412, 28, 40, 22, idLocalGo)
	winapi.Control(f.h, "BUTTON", "Up", winapi.WS_TABSTOP, 456, 28, 40, 22, idLocalUp)
	f.rp = winapi.ControlEx(f.h, winapi.WS_EX_CLIENTEDGE, "EDIT", "", winapi.ES_AUTOHSCROLL|winapi.WS_TABSTOP, 560, 28, 330, 22, idRemotePath)
	winapi.Control(f.h, "BUTTON", "Go", winapi.WS_TABSTOP, 894, 28, 40, 22, idRemoteGo)
	winapi.Control(f.h, "BUTTON", "Up", winapi.WS_TABSTOP, 938, 28, 40, 22, idRemoteUp)
	winapi.Control(f.h, "BUTTON", "Refresh", winapi.WS_TABSTOP, 982, 28, 70, 22, idRemoteRefresh)
	f.lst = winapi.NewListView(f.h, idLocalList, 8, 56, 490, 380, true, "Name", 300, "Size", 90, "Type", 80)
	f.rst = winapi.NewListView(f.h, idRemoteList, 560, 56, 490, 380, true, "Name", 300, "Size", 90, "Type", 80)
	f.send = winapi.Control(f.h, "BUTTON", "Send →", winapi.WS_TABSTOP, 504, 180, 50, 30, idSend)
	f.recv = winapi.Control(f.h, "BUTTON", "← Receive", winapi.WS_TABSTOP, 504, 220, 50, 30, idReceive)
	f.hint = winapi.Control(f.h, "STATIC", "Select files or folders and press Send or Receive, or drop files from Explorer onto this window to send them to the remote folder shown. Received files are never opened automatically.", winapi.SS_LEFT, 8, 442, 1040, 34, 0)
	f.q = winapi.NewListView(f.h, idQueue, 8, 480, 960, 170, false, "Transfer", 260, "Direction", 70, "Progress", 120, "Files", 80, "State", 140, "Detail", 280)
	f.cancel = winapi.Control(f.h, "BUTTON", "Cancel", winapi.WS_TABSTOP, 974, 480, 80, 26, idCancel)
	f.resume = winapi.Control(f.h, "BUTTON", "Resume", winapi.WS_TABSTOP, 974, 512, 80, 26, idResume)
	winapi.RegisterDialog(f.h)
	f.loadLocal(f.localDir)
	f.permsChanged()
	f.listRemote("")
	return f
}

func (f *fileManager) show() {
	winapi.ShowWindow(f.h, winapi.SW_SHOW)
	winapi.SetForeground(f.h)
}

func (f *fileManager) permsChanged() {
	ok := f.a.c != nil && f.a.c.Has(protocol.PermTransfer)
	for _, h := range []windows.HWND{f.send, f.recv, f.resume} {
		winapi.Enable(h, ok)
	}
	if !ok {
		winapi.SetText(f.hint, "File transfer permission is not granted in this session (or was removed by the person at the remote PC). Running transfers were stopped.")
	}
}

func (f *fileManager) layout() {
	r := winapi.ClientRect(f.h)
	w := r.Right
	half := (w - 80) / 2
	listH := r.Bottom - 56 - 250
	if listH < 120 {
		listH = 120
	}
	rx := 8 + half + 64
	winapi.MoveWindow(f.lst.H, 8, 56, half, listH)
	winapi.MoveWindow(f.rst.H, rx, 56, half, listH)
	winapi.MoveWindow(f.send, 8+half+4, 56+listH/2-34, 56, 30)
	winapi.MoveWindow(f.recv, 8+half+4, 56+listH/2+4, 56, 30)
	winapi.MoveWindow(f.lp, 8, 28, half-96, 22)
	winapi.MoveWindow(f.rp, rx, 28, half-170, 22)
	y := 56 + listH + 6
	winapi.MoveWindow(f.hint, 8, y, w-16, 34)
	winapi.MoveWindow(f.q.H, 8, y+38, w-108, r.Bottom-y-46)
	winapi.MoveWindow(f.cancel, w-94, y+38, 86, 26)
	winapi.MoveWindow(f.resume, w-94, y+70, 86, 26)
	f.moveButtons(rx, half)
}

func (f *fileManager) moveButtons(rx, half int32) {
	// The Go/Up buttons are found by id.
	move := func(id int, x int32, w int32) {
		if c := getDlgItem(f.h, id); c != 0 {
			winapi.MoveWindow(c, x, 28, w, 22)
		}
	}
	move(idLocalGo, 8+half-92, 40)
	move(idLocalUp, 8+half-48, 40)
	move(idRemoteGo, rx+half-166, 40)
	move(idRemoteUp, rx+half-122, 40)
	move(idRemoteRefresh, rx+half-78, 74)
}

var pGetDlgItem = windows.NewLazySystemDLL("user32.dll").NewProc("GetDlgItem")

func getDlgItem(h windows.HWND, id int) windows.HWND {
	r, _, _ := pGetDlgItem.Call(uintptr(h), uintptr(id))
	return windows.HWND(r)
}

func (f *fileManager) proc(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case winapi.WM_SIZE:
		f.layout()
		return 0, true
	case winapi.WM_CLOSE:
		winapi.ShowWindow(h, winapi.SW_HIDE) // transfers keep running
		return 0, true
	case winapi.WM_DROPFILES:
		paths := winapi.DroppedFiles(wp)
		f.upload(paths)
		return 0, true
	case winapi.WM_NOTIFY:
		n := (*winapi.NMHDR)(unsafe.Pointer(lp))
		if n.Code == winapi.NM_DBLCLK {
			switch int(n.ID) {
			case idLocalList:
				f.openLocal()
			case idRemoteList:
				f.openRemote()
			}
		}
		return 0, false
	case winapi.WM_COMMAND:
		switch int(wp & 0xffff) {
		case idSend:
			f.sendSelected()
		case idReceive:
			f.receiveSelected()
		case idLocalUp:
			if p := filepath.Dir(f.localDir); p != f.localDir {
				f.loadLocal(p)
			} else {
				f.loadLocal("")
			}
		case idLocalGo:
			f.loadLocal(strings.TrimSpace(winapi.GetText(f.lp)))
		case idRemoteGo:
			f.listRemote(strings.TrimSpace(winapi.GetText(f.rp)))
		case idRemoteUp:
			if f.remoteDir != "" {
				p := filepath.Dir(f.remoteDir)
				if p == f.remoteDir {
					p = ""
				}
				f.listRemote(p)
			}
		case idRemoteRefresh:
			f.listRemote(f.remoteDir)
		case idCancel:
			if id := f.selectedJob(); id != "" && f.a.c != nil {
				go f.a.c.Xfer.Cancel(id)
			}
		case idResume:
			if id := f.selectedJob(); id != "" && f.a.c != nil {
				p := f.jobs[id]
				if p.State != transfer.StateInterrupted && p.State != transfer.StateFailed && p.State != transfer.StateCancelled {
					winapi.MessageBox(h, "Only interrupted, failed or cancelled transfers can be resumed.", title, winapi.MB_ICONINFORMATION)
					break
				}
				c := f.a.c
				go func() {
					if _, err := c.Resume(id); err != nil {
						f.a.ui.Post(func() { winapi.MessageBox(h, "Could not resume: "+err.Error(), title, winapi.MB_ICONERROR) })
					}
				}()
			}
		}
		return 0, true
	}
	return 0, false
}

// ------------------------------------------------------------ local pane

func (f *fileManager) loadLocal(dir string) {
	f.local = f.local[:0]
	if dir == "" {
		for _, d := range winapi.FixedDisks() {
			f.local = append(f.local, entry{name: d.Name, dir: true})
		}
		for _, k := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Desktop, windows.FOLDERID_Documents, windows.FOLDERID_Downloads} {
			if p := winapi.KnownFolder(k); p != "" {
				f.local = append(f.local, entry{name: p, dir: true})
			}
		}
	} else {
		ents, err := os.ReadDir(dir)
		if err != nil {
			winapi.MessageBox(f.h, "Cannot open that folder on this PC.", title, winapi.MB_ICONWARNING)
			return
		}
		f.local = append(f.local, entry{name: "..", dir: true, up: true})
		for _, e := range ents {
			info, err := e.Info()
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			f.local = append(f.local, entry{name: e.Name(), dir: e.IsDir(), size: info.Size()})
		}
		sortEntries(f.local[1:])
	}
	f.localDir = dir
	winapi.SetText(f.lp, dir)
	fill(f.lst, f.local)
}

func (f *fileManager) openLocal() {
	sel := f.lst.Selected()
	if len(sel) != 1 || !f.local[sel[0]].dir {
		return
	}
	e := f.local[sel[0]]
	switch {
	case e.up:
		p := filepath.Dir(f.localDir)
		if p == f.localDir {
			p = ""
		}
		f.loadLocal(p)
	case f.localDir == "":
		f.loadLocal(e.name)
	default:
		f.loadLocal(filepath.Join(f.localDir, e.name))
	}
}

// ------------------------------------------------------------ remote pane

func (f *fileManager) listRemote(path string) {
	if c := f.a.c; c != nil && c.Has(protocol.PermTransfer) {
		go func() { _ = c.ListRemote(path) }()
	}
}

func (f *fileManager) remoteListing(l viewercore.Listing) {
	if l.Error != "" {
		winapi.MessageBox(f.h, "Remote PC: "+l.Error, title, winapi.MB_ICONWARNING)
		return
	}
	f.remote = f.remote[:0]
	if l.Path != "" {
		f.remote = append(f.remote, entry{name: "..", dir: true, up: true})
	}
	for _, e := range l.Entries {
		f.remote = append(f.remote, entry{name: e.Name, dir: e.Dir, size: e.Size})
	}
	f.remoteDir = l.Path
	winapi.SetText(f.rp, l.Path)
	fill(f.rst, f.remote)
}

func (f *fileManager) openRemote() {
	sel := f.rst.Selected()
	if len(sel) != 1 || !f.remote[sel[0]].dir {
		return
	}
	e := f.remote[sel[0]]
	switch {
	case e.up:
		p := filepath.Dir(f.remoteDir)
		if p == f.remoteDir {
			p = ""
		}
		f.listRemote(p)
	case f.remoteDir == "":
		f.listRemote(e.name)
	default:
		f.listRemote(filepath.Join(f.remoteDir, e.name))
	}
}

// ------------------------------------------------------------ transfers

func (f *fileManager) sendSelected() {
	var paths []string
	for _, i := range f.lst.Selected() {
		e := f.local[i]
		if e.up || f.localDir == "" {
			continue
		}
		paths = append(paths, filepath.Join(f.localDir, e.name))
	}
	f.upload(paths)
}

func (f *fileManager) upload(paths []string) {
	c := f.a.c
	if len(paths) == 0 || c == nil {
		return
	}
	if f.remoteDir == "" {
		winapi.MessageBox(f.h, "Open a folder on the remote PC first (for example Downloads).", title, winapi.MB_ICONINFORMATION)
		return
	}
	dest := f.remoteDir
	go func() {
		if _, err := c.Upload(paths, dest); err != nil {
			f.a.ui.Post(func() { winapi.MessageBox(f.h, "Could not send: "+err.Error(), title, winapi.MB_ICONERROR) })
		}
	}()
}

func (f *fileManager) receiveSelected() {
	c := f.a.c
	if c == nil {
		return
	}
	if f.localDir == "" {
		winapi.MessageBox(f.h, "Open a folder on this PC first.", title, winapi.MB_ICONINFORMATION)
		return
	}
	var paths []string
	for _, i := range f.rst.Selected() {
		e := f.remote[i]
		if e.up || f.remoteDir == "" {
			continue
		}
		paths = append(paths, filepath.Join(f.remoteDir, e.name))
	}
	if len(paths) == 0 {
		return
	}
	dest := f.localDir
	go func() {
		if _, err := c.Download(paths, dest); err != nil {
			f.a.ui.Post(func() { winapi.MessageBox(f.h, "Could not receive: "+err.Error(), title, winapi.MB_ICONERROR) })
		}
	}()
}

func (f *fileManager) progress(p transfer.Progress) {
	_, known := f.jobs[p.JobID]
	f.jobs[p.JobID] = p
	row, ok := f.rows[p.JobID]
	if !ok || !known {
		row = f.q.Add(p.Label)
		f.rows[p.JobID] = row
		f.order = append(f.order, p.JobID)
	}
	dir := "Send"
	if p.Direction == "receive" {
		dir = "Receive"
	}
	pct := "—"
	if p.BytesTotal > 0 {
		pct = fmt.Sprintf("%d%% of %s", p.BytesDone*100/p.BytesTotal, human(p.BytesTotal))
	} else if p.State == transfer.StateDone {
		pct = "100%"
	}
	files := fmt.Sprintf("%d / %d", p.FilesDone, p.Files)
	detail := p.Current
	if p.Error != "" {
		detail = p.Error
	} else if p.State == transfer.StateDone {
		detail = "Saved to " + p.Dest
		if p.Skipped > 0 {
			detail += fmt.Sprintf(" (%d skipped)", p.Skipped)
		}
	}
	f.q.Set(row, 0, p.Label)
	f.q.Set(row, 1, dir)
	f.q.Set(row, 2, pct)
	f.q.Set(row, 3, files)
	f.q.Set(row, 4, string(p.State))
	f.q.Set(row, 5, detail)
	if p.State == transfer.StateDone && p.Direction == "receive" && strings.EqualFold(filepath.Clean(p.Dest), filepath.Clean(f.localDir)) {
		f.loadLocal(f.localDir)
	}
	if p.State == transfer.StateDone && p.Direction == "send" && f.remoteDir != "" {
		f.listRemote(f.remoteDir)
	}
}

func (f *fileManager) selectedJob() string {
	sel := f.q.Selected()
	if len(sel) != 1 {
		return ""
	}
	for id, r := range f.rows {
		if r == sel[0] {
			return id
		}
	}
	return ""
}

func fill(lv *winapi.ListView, es []entry) {
	lv.Clear()
	for _, e := range es {
		size, kind := human(e.size), "File"
		if e.dir {
			size, kind = "", "Folder"
		}
		lv.Add(e.name, size, kind)
	}
}

func sortEntries(es []entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].dir != es[j].dir {
			return es[i].dir
		}
		return strings.ToLower(es[i].name) < strings.ToLower(es[j].name)
	})
}

func human(n int64) string {
	const k = 1024
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	case n >= k:
		return fmt.Sprintf("%.0f KB", float64(n)/k)
	}
	return fmt.Sprintf("%d B", n)
}

// ------------------------------------------------------------ dialogs

// conflictDialog asks what to do with an existing file. It is modeless so
// the session keeps running; reply is called exactly once.
func conflictDialog(owner windows.HWND, ci transfer.ConflictInfo, reply func(string, bool)) {
	winapi.RegisterClass("RSMConflict", uintptr(winapi.COLOR_BTNFACE+1))
	var all windows.HWND
	answered := false
	var h windows.HWND
	answer := func(action string) {
		if answered {
			return
		}
		answered = true
		applyAll := winapi.Checked(all)
		winapi.UnregisterDialog(h)
		winapi.DestroyWindow(h)
		go reply(action, applyAll)
	}
	h = winapi.CreateWindow(winapi.WS_EX_DLGMODALFRAME|winapi.WS_EX_TOPMOST, "RSMConflict", "File already exists", winapi.WS_CAPTION|winapi.WS_SYSMENU|winapi.WS_VISIBLE,
		cwDefault, cwDefault, 520, 230, owner, 0,
		func(hh windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
			switch msg {
			case winapi.WM_COMMAND:
				switch wp & 0xffff {
				case 11:
					answer("overwrite")
				case 12, winapi.IDOK: // Enter picks the default, Keep both
					answer("rename")
				case 13, winapi.IDCANCEL:
					answer("skip")
				}
				return 0, true
			case winapi.WM_CLOSE:
				answer("skip")
				return 0, true
			}
			return 0, false
		})
	text := fmt.Sprintf("\"%s\" already exists at the destination.\n\nExisting: %s, modified %s\nIncoming: %s",
		ci.Rel, human(ci.ExistingSize), ci.ExistingTime.Format(time.DateTime), human(ci.NewSize))
	winapi.Control(h, "STATIC", text, winapi.SS_LEFT, 12, 12, 490, 80, 0)
	all = winapi.Control(h, "BUTTON", "Do this for the remaining conflicts in this transfer", winapi.BS_AUTOCHECKBOX|winapi.WS_TABSTOP, 12, 98, 490, 22, 4)
	winapi.Control(h, "BUTTON", "Replace", winapi.WS_TABSTOP, 12, 134, 150, 30, 11)
	winapi.Control(h, "BUTTON", "Keep both (rename)", winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, 172, 134, 170, 30, 12)
	winapi.Control(h, "BUTTON", "Skip", winapi.WS_TABSTOP, 352, 134, 150, 30, 13)
	winapi.RegisterDialog(h)
	winapi.SetForeground(h)
}

// promptText shows a one-line input box and calls done with the text, or
// with "" if cancelled.
func promptText(owner windows.HWND, label, def string, done func(string)) {
	winapi.RegisterClass("RSMPrompt", uintptr(winapi.COLOR_BTNFACE+1))
	var edit, h windows.HWND
	finished := false
	finish := func(s string) {
		if finished {
			return
		}
		finished = true
		winapi.UnregisterDialog(h)
		winapi.DestroyWindow(h)
		done(s)
	}
	h = winapi.CreateWindow(winapi.WS_EX_DLGMODALFRAME, "RSMPrompt", title, winapi.WS_CAPTION|winapi.WS_SYSMENU|winapi.WS_VISIBLE,
		cwDefault, cwDefault, 620, 170, owner, 0,
		func(hh windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
			switch msg {
			case winapi.WM_COMMAND:
				switch wp & 0xffff {
				case winapi.IDOK:
					finish(strings.TrimSpace(winapi.GetText(edit)))
				case winapi.IDCANCEL:
					finish("")
				}
				return 0, true
			case winapi.WM_CLOSE:
				finish("")
				return 0, true
			}
			return 0, false
		})
	winapi.Control(h, "STATIC", label, winapi.SS_LEFT, 12, 12, 590, 20, 0)
	edit = winapi.ControlEx(h, winapi.WS_EX_CLIENTEDGE, "EDIT", def, winapi.ES_AUTOHSCROLL|winapi.WS_TABSTOP, 12, 38, 580, 24, 5)
	winapi.Control(h, "BUTTON", "Connect", winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, 392, 76, 96, 28, winapi.IDOK)
	winapi.Control(h, "BUTTON", "Cancel", winapi.WS_TABSTOP, 496, 76, 96, 28, winapi.IDCANCEL)
	winapi.RegisterDialog(h)
	winapi.SetFocus(edit)
}
