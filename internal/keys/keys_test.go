package keys

import (
	"testing"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

func TestReleaseAllReleasesEverythingModifiersLast(t *testing.T) {
	tr := NewTracker()
	tr.Observe(key(ScanCtrl, false, VKLControl, true))
	tr.Observe(key(ScanWin, true, VKLWin, true))
	tr.Observe(key(ScanR, false, VKR, true))
	tr.Observe(key(ScanR, false, VKR, false))
	tr.Observe(key(ScanTab, false, VKTab, true))
	ups := tr.ReleaseAll()
	if len(ups) != 3 {
		t.Fatalf("want 3 releases, got %d", len(ups))
	}
	for _, u := range ups {
		if u.Down {
			t.Fatal("release must be key up")
		}
	}
	if isModifier(ups[0]) || !isModifier(ups[2]) {
		t.Fatalf("modifiers should be released last: %+v", ups)
	}
	if tr.Held() != 0 || len(tr.ReleaseAll()) != 0 {
		t.Fatal("state not cleared")
	}
}

func TestExtendedKeysTrackedSeparately(t *testing.T) {
	tr := NewTracker()
	tr.Observe(key(ScanCtrl, false, VKLControl, true)) // left ctrl
	tr.Observe(key(ScanCtrl, true, 0xA3, true))        // right ctrl
	tr.Observe(key(ScanCtrl, false, VKLControl, false))
	if tr.Held() != 1 {
		t.Fatal("right ctrl should still be held")
	}
}

func TestCombosBalanced(t *testing.T) {
	for _, name := range protocol.Combos {
		ev, ok := ComboEvents(name)
		if name == "ctrl-alt-del" {
			if ok {
				t.Fatal("ctrl-alt-del must use the secure attention sequence")
			}
			continue
		}
		if !ok || len(ev) == 0 {
			t.Fatalf("combo %s missing", name)
		}
		tr := NewTracker()
		for _, k := range ev {
			tr.Observe(k)
		}
		if tr.Held() != 0 {
			t.Fatalf("combo %s leaves keys held", name)
		}
	}
}

func TestEscapeShortcut(t *testing.T) {
	var e Escape
	if e.Check(key(ScanF12, false, VKF12, true)) {
		t.Fatal("F12 alone must not escape")
	}
	e.Check(key(ScanCtrl, false, VKLControl, true))
	e.Check(key(ScanAlt, false, VKLMenu, true))
	if !e.Check(key(ScanF12, false, VKF12, true)) {
		t.Fatal("Ctrl+Alt+F12 must escape")
	}
	e.Reset()
	if e.Check(key(ScanF12, false, VKF12, true)) {
		t.Fatal("reset failed")
	}
}
