package clipsync

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// loopback delivers synchronously to the other side.
type loopback struct {
	to   *Sync
	sent int
}

func (l *loopback) Send(h any, p []byte) error {
	b, err := protocol.Encode(h, p)
	if err != nil {
		return err
	}
	m, _ := protocol.Decode(b)
	l.sent++
	return l.to.Handle(m)
}

type fakeClipboard struct {
	mu   sync.Mutex
	c    Clip
	sync *Sync // the listener: writing fires LocalChanged like the OS would
}

func (f *fakeClipboard) set(c Clip) {
	f.mu.Lock()
	f.c = c
	f.mu.Unlock()
	_, _ = f.sync.LocalChanged(c)
}

func setup(t *testing.T) (viewer, agent *Sync, vClip, aClip *fakeClipboard, v2a, a2v *loopback, audit *[]string) {
	v2a, a2v = &loopback{}, &loopback{}
	viewer = New(v2a, true)
	agent = New(a2v, false)
	v2a.to, a2v.to = agent, viewer
	vClip = &fakeClipboard{sync: viewer}
	aClip = &fakeClipboard{sync: agent}
	viewer.Apply = func(c Clip) error { vClip.set(c); return nil }
	agent.Apply = func(c Clip) error { aClip.set(c); return nil }
	var log []string
	rec := func(ev string, f map[string]any) { log = append(log, fmt.Sprint(ev, f)) }
	viewer.Audit, agent.Audit = rec, rec
	viewer.SetEpoch(1)
	agent.SetEpoch(1)
	return viewer, agent, vClip, aClip, v2a, a2v, &log
}

func TestAllFormatsBothDirectionsNoLoop(t *testing.T) {
	viewer, _, vClip, aClip, v2a, a2v, audit := setup(t)
	if err := viewer.Configure(true, Both); err != nil {
		t.Fatal(err)
	}
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1000)
	clip := Clip{
		Text: "Grüße 👋\r\nline two\nline three — ✓",
		RTF:  []byte(`{\rtf1\ansi {\b bold} text}`),
		HTML: []byte("Version:0.9\r\n<b>bold</b>"),
		PNG:  png,
	}
	vClip.set(clip)
	if aClip.c.Text != clip.Text || !bytes.Equal(aClip.c.RTF, clip.RTF) || !bytes.Equal(aClip.c.HTML, clip.HTML) || !bytes.Equal(aClip.c.PNG, png) {
		t.Fatal("viewer -> agent formats not preserved")
	}
	// The agent's write fired its listener, which must not echo back.
	if a2v.sent != 0 {
		t.Fatalf("loop: agent echoed %d messages", a2v.sent)
	}
	aClip.set(Clip{Text: "copied on the endpoint"})
	if vClip.c.Text != "copied on the endpoint" {
		t.Fatal("agent -> viewer failed")
	}
	if v2a.sent != 2 { // config + first clip; no echo
		t.Fatalf("loop: viewer sent %d messages", v2a.sent)
	}
	for _, l := range *audit {
		if strings.Contains(l, "Grüße") || strings.Contains(l, "bold") || strings.Contains(l, "endpoint") {
			t.Fatalf("clipboard content leaked into audit: %s", l)
		}
	}
}

func TestDirectionControls(t *testing.T) {
	viewer, _, vClip, aClip, _, _, _ := setup(t)
	_ = viewer.Configure(true, ToRemote)
	aClip.set(Clip{Text: "remote secret"})
	if vClip.c.Text != "" {
		t.Fatal("to-remote must not copy endpoint clipboard to technician")
	}
	vClip.set(Clip{Text: "to remote"})
	if aClip.c.Text != "to remote" {
		t.Fatal("to-remote failed")
	}
	_ = viewer.Configure(true, ToLocal)
	vClip.set(Clip{Text: "should stay local"})
	if aClip.c.Text != "to remote" {
		t.Fatal("to-local must not send technician clipboard")
	}
	_ = viewer.Configure(false, Both)
	aClip.set(Clip{Text: "off"})
	vClip.set(Clip{Text: "off too"})
	if aClip.c.Text != "off" || vClip.c.Text != "off too" {
		t.Fatal("fake clipboards should hold local values")
	}
}

func TestStaleEpochDroppedAfterReconnect(t *testing.T) {
	viewer, agent, _, aClip, _, _, _ := setup(t)
	_ = viewer.Configure(true, Both)
	// A clip queued before the reconnect (epoch 1) arrives after it (epoch 2).
	stale, _ := protocol.Encode(protocol.ClipData{T: protocol.TClipData, Epoch: 1, Hash: Clip{Text: "old"}.Hash(), Formats: []protocol.ClipFormat{{F: "text", Len: 3}}}, []byte("old"))
	agent.SetEpoch(2)
	viewer.SetEpoch(2)
	m, _ := protocol.Decode(stale)
	_ = agent.Handle(m)
	if aClip.c.Text == "old" {
		t.Fatal("stale paste applied after reconnect")
	}
}

func TestStopEndsSync(t *testing.T) {
	viewer, agent, vClip, aClip, _, _, _ := setup(t)
	_ = viewer.Configure(true, Both)
	agent.Stop()
	viewer.Stop()
	vClip.set(Clip{Text: "after end"})
	if aClip.c.Text != "" {
		t.Fatal("sync continued after session end")
	}
	if en, _ := viewer.State(); en {
		t.Fatal("still enabled")
	}
	// Configure after stop cannot re-enable.
	_ = viewer.Configure(true, Both)
	if en, _ := viewer.State(); en {
		t.Fatal("re-enabled after stop")
	}
}

func TestFilesRoutedNotSent(t *testing.T) {
	viewer, agent, vClip, _, _, _, _ := setup(t)
	_ = viewer.Configure(true, Both)
	var offered []string
	agent.OnFiles = func(p []string) { offered = p }
	vClip.set(Clip{Files: []string{`C:\Users\tech\Desktop\logs`}})
	if len(offered) != 1 {
		t.Fatal("files should be offered to the transfer queue")
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	_, agent, _, aClip, _, _, _ := setup(t)
	_ = agent.Configure(true, Both)
	b, _ := protocol.Encode(protocol.ClipData{T: protocol.TClipData, Epoch: 1, Hash: Clip{Text: "a"}.Hash(), Formats: []protocol.ClipFormat{{F: "text", Len: 1}}}, []byte("b"))
	m, _ := protocol.Decode(b)
	if err := agent.Handle(m); err == nil || aClip.c.Text != "" {
		t.Fatal("mismatched hash must be rejected")
	}
	b, _ = protocol.Encode(protocol.ClipData{T: protocol.TClipData, Epoch: 1, Formats: []protocol.ClipFormat{{F: "text", Len: 99}}}, []byte("b"))
	m, _ = protocol.Decode(b)
	if err := agent.Handle(m); err == nil {
		t.Fatal("bad length must be rejected")
	}
}
