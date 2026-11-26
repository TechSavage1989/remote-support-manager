//go:build windows

package winapi

import (
	"errors"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000
	mouseVirtual    = 0x4000
	mouseAbsolute   = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyUnicode  = 0x0004
	keyScancode = 0x0008
)

// INPUT is the 64-bit layout of the Win32 INPUT union (40 bytes).
type mouseInput struct {
	Type      uint32
	_         uint32
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	_         uint32
	Extra     uintptr
}

type keybdInput struct {
	Type  uint32
	_     uint32
	VK    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	_     uint32
	Extra uintptr
	_     [8]byte
}

func sendInputs(p unsafe.Pointer, n int, size uintptr) error {
	sent := call(pSendInput, uintptr(n), uintptr(p), size)
	if int(sent) != n {
		return errors.New("SendInput was blocked (the target window may run as administrator or on the secure desktop)")
	}
	return nil
}

// InjectMouse moves or clicks on the given monitor.
func InjectMouse(m protocol.Mouse, mon protocol.Monitor) error {
	vx, vy := SystemMetric(SM_XVIRTUALSCREEN), SystemMetric(SM_YVIRTUALSCREEN)
	vw, vh := SystemMetric(SM_CXVIRTUALSCREEN), SystemMetric(SM_CYVIRTUALSCREEN)
	x := int32(mon.X+clamp(m.X, 0, mon.W-1)) - vx
	y := int32(mon.Y+clamp(m.Y, 0, mon.H-1)) - vy
	in := mouseInput{Type: inputMouse, Flags: mouseMove | mouseAbsolute | mouseVirtual}
	if vw > 1 && vh > 1 {
		in.Dx = int32(int64(x) * 65535 / int64(vw-1))
		in.Dy = int32(int64(y) * 65535 / int64(vh-1))
	}
	switch m.Action {
	case "down", "up":
		f := map[string][2]uint32{"left": {mouseLeftDown, mouseLeftUp}, "right": {mouseRightDown, mouseRightUp}, "middle": {mouseMiddleDown, mouseMiddleUp}}[m.Button]
		if f[0] == 0 {
			return nil
		}
		if m.Action == "down" {
			in.Flags |= f[0]
		} else {
			in.Flags |= f[1]
		}
	case "wheel":
		in.Flags |= mouseWheel
		in.MouseData = uint32(int32(clamp(m.Delta, -1200, 1200)))
	case "hwheel":
		in.Flags |= mouseHWheel
		in.MouseData = uint32(int32(clamp(m.Delta, -1200, 1200)))
	}
	return sendInputs(unsafe.Pointer(&in), 1, unsafe.Sizeof(in))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// InjectKey presses or releases a key by scan code, so the endpoint's own
// keyboard layout decides the character.
func InjectKey(k protocol.Key) error {
	in := keybdInput{Type: inputKeyboard, Scan: k.Scan, Flags: keyScancode}
	if k.Ext {
		in.Flags |= keyExtended
	}
	if !k.Down {
		in.Flags |= keyUp
	}
	if k.Scan == 0 { // keys without a scan code fall back to the virtual key
		in.Flags &^= keyScancode
		in.VK = k.VK
	}
	return sendInputs(unsafe.Pointer(&in), 1, unsafe.Sizeof(in))
}

// InjectText types text as Unicode keystrokes (Paste as Keystrokes).
// Line breaks are sent as Enter, tabs as Tab.
func InjectText(s string) error {
	var ins []keybdInput
	prevCR := false
	for _, r := range s {
		switch r {
		case '\r':
			prevCR = true
			ins = append(ins, keybdInput{Type: inputKeyboard, VK: 0x0D}, keybdInput{Type: inputKeyboard, VK: 0x0D, Flags: keyUp})
			continue
		case '\n':
			if prevCR {
				prevCR = false
				continue
			}
			ins = append(ins, keybdInput{Type: inputKeyboard, VK: 0x0D}, keybdInput{Type: inputKeyboard, VK: 0x0D, Flags: keyUp})
			continue
		case '\t':
			ins = append(ins, keybdInput{Type: inputKeyboard, VK: 0x09}, keybdInput{Type: inputKeyboard, VK: 0x09, Flags: keyUp})
			continue
		}
		prevCR = false
		for _, u := range utf16.Encode([]rune{r}) {
			ins = append(ins, keybdInput{Type: inputKeyboard, Scan: u, Flags: keyUnicode}, keybdInput{Type: inputKeyboard, Scan: u, Flags: keyUnicode | keyUp})
		}
	}
	for len(ins) > 0 {
		n := len(ins)
		if n > 200 {
			n = 200
		}
		if err := sendInputs(unsafe.Pointer(&ins[0]), n, unsafe.Sizeof(ins[0])); err != nil {
			return err
		}
		ins = ins[n:]
	}
	return nil
}

// SendSAS asks Windows to deliver Ctrl+Alt+Del. Windows only honours this
// from a process running as LocalSystem (the unattended-access session
// helper) and when the "software Secure Attention Sequence" policy allows
// services; the unattended installer turns that policy on. Attended and
// support clients run as the signed-in user, where this reports that it is
// unavailable.
func SendSAS() error {
	if err := pSendSAS.Find(); err != nil {
		return errors.New("Ctrl+Alt+Del is not available: sas.dll is missing on this Windows edition")
	}
	if !IsLocalSystem() {
		return errors.New("Ctrl+Alt+Del is only available with unattended access; use Ctrl+Shift+Esc for Task Manager")
	}
	call(pSendSAS, 0)
	return nil
}

// ------------------------------------------------------------ keyboard hook

// KBDLLHOOKSTRUCT for WH_KEYBOARD_LL.
type KBDLLHOOKSTRUCT struct {
	VK, Scan, Flags, Time uint32
	Extra                 uintptr
}

// Hook is a low-level keyboard hook.
type Hook uintptr

// InstallKeyboardHook installs WH_KEYBOARD_LL on the calling (UI) thread.
// fn returns true to swallow the key.
func InstallKeyboardHook(fn func(msg uintptr, k *KBDLLHOOKSTRUCT) bool) Hook {
	cb := newHookCallback(fn)
	return Hook(call(pSetWindowsHookExW, 13 /* WH_KEYBOARD_LL */, cb, uintptr(Instance()), 0))
}

// Unhook removes the hook.
func (h Hook) Unhook() {
	if h != 0 {
		call(pUnhookWindowsHookEx, uintptr(h))
	}
}

var hookFn func(msg uintptr, k *KBDLLHOOKSTRUCT) bool

func hookProc(code int32, wp, lp uintptr) uintptr {
	if code >= 0 && hookFn != nil {
		if hookFn(wp, (*KBDLLHOOKSTRUCT)(unsafe.Pointer(lp))) {
			return 1
		}
	}
	return call(pCallNextHookEx, 0, uintptr(code), wp, lp)
}

var hookCB uintptr

func newHookCallback(fn func(msg uintptr, k *KBDLLHOOKSTRUCT) bool) uintptr {
	hookFn = fn
	if hookCB == 0 {
		hookCB = syscall.NewCallback(hookProc)
	}
	return hookCB
}
