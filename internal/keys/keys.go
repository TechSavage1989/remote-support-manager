// Package keys tracks keyboard state for remote input so that no key is ever
// left held down on either machine, and defines the reserved-key toolbar
// combinations.
//
// Keys are sent as hardware scan codes (set 1) with an extended flag plus the
// virtual-key code. The endpoint injects the scan code, so the endpoint's own
// keyboard layout decides which character is produced; that is what makes
// non-US layouts behave like a local keyboard on the remote PC.
package keys

import (
	"sort"
	"sync"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// Scan codes and virtual keys used below.
const (
	ScanEsc    = 0x01
	ScanTab    = 0x0F
	ScanCtrl   = 0x1D
	ScanShift  = 0x2A
	ScanAlt    = 0x38
	ScanF4     = 0x3E
	ScanF12    = 0x58
	ScanR      = 0x13
	ScanL      = 0x26
	ScanDelete = 0x53 // extended
	ScanWin    = 0x5B // extended
	ScanPrtSc  = 0x37 // extended

	VKTab      = 0x09
	VKEscape   = 0x1B
	VKSnapshot = 0x2C
	VKDelete   = 0x2E
	VKR        = 0x52
	VKL        = 0x4C
	VKLWin     = 0x5B
	VKF4       = 0x73
	VKF12      = 0x7B
	VKLShift   = 0xA0
	VKLControl = 0xA2
	VKLMenu    = 0xA4
)

func key(scan uint16, ext bool, vk uint16, down bool) protocol.Key {
	return protocol.Key{T: protocol.TInKey, Scan: scan, Ext: ext, VK: vk, Down: down}
}

// press returns down events for keys in order and up events in reverse.
func press(ks ...protocol.Key) []protocol.Key {
	out := make([]protocol.Key, 0, len(ks)*2)
	for _, k := range ks {
		k.Down = true
		out = append(out, k)
	}
	for i := len(ks) - 1; i >= 0; i-- {
		k := ks[i]
		k.Down = false
		out = append(out, k)
	}
	return out
}

var (
	kCtrl  = key(ScanCtrl, false, VKLControl, true)
	kAlt   = key(ScanAlt, false, VKLMenu, true)
	kShift = key(ScanShift, false, VKLShift, true)
	kWin   = key(ScanWin, true, VKLWin, true)
)

// ComboEvents returns the key events for a toolbar combination. The second
// result is false for "ctrl-alt-del", which cannot be injected as keys and
// must use the Windows secure attention sequence instead.
func ComboEvents(name string) ([]protocol.Key, bool) {
	switch name {
	case "ctrl-alt-del":
		return nil, false
	case "ctrl-shift-esc":
		return press(kCtrl, kShift, key(ScanEsc, false, VKEscape, true)), true
	case "alt-tab":
		return press(kAlt, key(ScanTab, false, VKTab, true)), true
	case "win":
		return press(kWin), true
	case "win-r":
		return press(kWin, key(ScanR, false, VKR, true)), true
	case "win-l":
		return press(kWin, key(ScanL, false, VKL, true)), true
	case "alt-f4":
		return press(kAlt, key(ScanF4, false, VKF4, true)), true
	case "print-screen":
		return press(key(ScanPrtSc, true, VKSnapshot, true)), true
	}
	return nil, false
}

func id(k protocol.Key) uint32 {
	v := uint32(k.Scan)
	if k.Ext {
		v |= 1 << 16
	}
	return v
}

// Tracker remembers which keys are currently held.
type Tracker struct {
	mu   sync.Mutex
	held map[uint32]protocol.Key
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{held: map[uint32]protocol.Key{}} }

// Observe records an event.
func (t *Tracker) Observe(k protocol.Key) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if k.Down {
		t.held[id(k)] = k
	} else {
		delete(t.held, id(k))
	}
}

// Held reports whether any key is held.
func (t *Tracker) Held() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.held)
}

// ReleaseAll returns key-up events for every held key (modifiers last) and
// clears the state. Call it on focus loss, capture release, disconnect and
// session end.
func (t *Tracker) ReleaseAll() []protocol.Key {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]protocol.Key, 0, len(t.held))
	for _, k := range t.held {
		k.Down = false
		k.T = protocol.TInKey
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		mi, mj := isModifier(out[i]), isModifier(out[j])
		if mi != mj {
			return !mi
		}
		return id(out[i]) < id(out[j])
	})
	t.held = map[uint32]protocol.Key{}
	return out
}

func isModifier(k protocol.Key) bool {
	switch k.Scan {
	case ScanCtrl, ScanAlt, ScanShift, 0x36:
		return true
	case ScanWin, 0x5C:
		return k.Ext
	}
	return false
}

// Escape is the local shortcut that always releases keyboard capture:
// Ctrl+Alt+F12. It is checked before anything is forwarded and is never sent
// to the endpoint.
type Escape struct {
	ctrl, alt bool
}

// Check updates modifier state and reports whether k completes the escape
// shortcut. Modifier state comes from the local key events.
func (e *Escape) Check(k protocol.Key) bool {
	switch k.Scan {
	case ScanCtrl:
		e.ctrl = k.Down
	case ScanAlt:
		e.alt = k.Down
	case ScanF12:
		return k.Down && e.ctrl && e.alt
	}
	return false
}

// Reset clears modifier state (e.g. after focus loss).
func (e *Escape) Reset() { e.ctrl, e.alt = false, false }
