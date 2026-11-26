//go:build windows

package winapi

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type notifyIconData struct {
	Size            uint32
	Wnd             windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     uintptr
}

// TrayMessage is posted to the owner window for tray icon events.
const TrayMessage = WM_APP + 20

// Tray is a notification-area icon that keeps the agent visibly present.
type Tray struct {
	d notifyIconData
}

// NewTray adds an icon with a tooltip.
func NewTray(owner windows.HWND, tip string) *Tray {
	t := &Tray{}
	t.d.Size = uint32(unsafe.Sizeof(t.d))
	t.d.Wnd = owner
	t.d.ID = 1
	t.d.Flags = 0x1 | 0x2 | 0x4 // NIF_MESSAGE | NIF_ICON | NIF_TIP
	t.d.CallbackMessage = TrayMessage
	t.d.Icon = call(pLoadIconW, uintptr(Instance()), 1)
	if t.d.Icon == 0 {
		t.d.Icon = call(pLoadIconW, 0, IDI_APPLICATION)
	}
	copy(t.d.Tip[:len(t.d.Tip)-1], windows.StringToUTF16(tip))
	call(pShellNotifyIconW, 0 /* NIM_ADD */, uintptr(unsafe.Pointer(&t.d)))
	return t
}

// SetTip updates the tooltip.
func (t *Tray) SetTip(tip string) {
	t.d.Tip = [128]uint16{}
	copy(t.d.Tip[:len(t.d.Tip)-1], windows.StringToUTF16(tip))
	t.d.Flags = 0x4
	call(pShellNotifyIconW, 1 /* NIM_MODIFY */, uintptr(unsafe.Pointer(&t.d)))
}

// Balloon shows a notification.
func (t *Tray) Balloon(title, text string) {
	t.d.Flags = 0x10 // NIF_INFO
	t.d.Info = [256]uint16{}
	t.d.InfoTitle = [64]uint16{}
	copy(t.d.Info[:len(t.d.Info)-1], windows.StringToUTF16(text))
	copy(t.d.InfoTitle[:len(t.d.InfoTitle)-1], windows.StringToUTF16(title))
	t.d.InfoFlags = 0x1 // NIIF_INFO
	call(pShellNotifyIconW, 1, uintptr(unsafe.Pointer(&t.d)))
}

// Remove deletes the icon.
func (t *Tray) Remove() { call(pShellNotifyIconW, 2 /* NIM_DELETE */, uintptr(unsafe.Pointer(&t.d))) }

// DragAcceptFiles enables WM_DROPFILES on a window.
func DragAcceptFiles(h windows.HWND, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	call(pDragAcceptFiles, uintptr(h), v)
}

// DroppedFiles reads and releases a WM_DROPFILES handle.
func DroppedFiles(hdrop uintptr) []string {
	defer call(pDragFinish, hdrop)
	n := call(pDragQueryFileW, hdrop, 0xFFFFFFFF, 0, 0)
	out := []string{}
	for i := uintptr(0); i < n; i++ {
		l := call(pDragQueryFileW, hdrop, i, 0, 0)
		buf := make([]uint16, l+1)
		call(pDragQueryFileW, hdrop, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		out = append(out, windows.UTF16ToString(buf))
	}
	return out
}
