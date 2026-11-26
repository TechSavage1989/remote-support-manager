package agentcore

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// SimPlatform is a synthetic endpoint used by automated end-to-end tests and
// by "rsm-agent --simulate" on machines that are not Windows. It draws a
// generated screen, records injected input and keeps an in-memory clipboard.
// It never touches the real desktop.
type SimPlatform struct {
	mu        sync.Mutex
	Mons      []protocol.Monitor
	Keys      []protocol.Key
	MouseEv   []protocol.Mouse
	Texts     []string
	Combos    int
	clip      clipsync.Clip
	watchers  map[int]func()
	nextWatch int
	Received  []string
	Notes     []string
	FileRoot  string

	// Consent decides the endpoint user's answer. The default declines.
	Consent func(req protocol.SessionRequest, attempt int) ConsentResult
	// Indicators records shown indicators so tests can press End.
	Indicators []*SimIndicator
	frame      int
}

// NewSim returns a simulator with two monitors.
func NewSim(root string) *SimPlatform {
	return &SimPlatform{
		Mons: []protocol.Monitor{
			{Index: 0, Name: "SIM1", X: 0, Y: 0, W: 640, H: 360, Primary: true},
			{Index: 1, Name: "SIM2", X: 640, Y: 0, W: 320, H: 240},
		},
		watchers: map[int]func(){},
		FileRoot: root,
	}
}

func (p *SimPlatform) Inventory() Inventory {
	h, _ := os.Hostname()
	return Inventory{Hostname: "SIM-" + h, OSName: "Windows 11 Pro (simulated)", OSVersion: "10.0.26100", UptimeSec: 3600 * 5,
		Disks: []Disk{{Name: "C:", Total: 256 << 30, Free: 100 << 30}}, LoggedOnUser: `SIM\endpoint-user`}
}

func (p *SimPlatform) Monitors() []protocol.Monitor {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]protocol.Monitor(nil), p.Mons...)
}

// Capture draws a gradient with a box that moves every frame.
func (p *SimPlatform) Capture(mon protocol.Monitor) (*image.RGBA, error) {
	p.mu.Lock()
	p.frame++
	f := p.frame
	p.mu.Unlock()
	img := image.NewRGBA(image.Rect(0, 0, mon.W, mon.H))
	for y := 0; y < mon.H; y++ {
		for x := 0; x < mon.W; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / max(mon.W, 1)), uint8(y * 255 / max(mon.H, 1)), uint8(mon.Index * 120), 255})
		}
	}
	bx := (f * 7) % max(mon.W-20, 1)
	for y := 10; y < 30 && y < mon.H; y++ {
		for x := bx; x < bx+20 && x < mon.W; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	return img, nil
}

func (p *SimPlatform) InjectMouse(m protocol.Mouse, _ protocol.Monitor) error {
	p.mu.Lock()
	p.MouseEv = append(p.MouseEv, m)
	p.mu.Unlock()
	return nil
}

func (p *SimPlatform) InjectKey(k protocol.Key) error {
	p.mu.Lock()
	p.Keys = append(p.Keys, k)
	p.mu.Unlock()
	return nil
}

func (p *SimPlatform) InjectText(t string) error {
	p.mu.Lock()
	p.Texts = append(p.Texts, t)
	p.mu.Unlock()
	return nil
}

func (p *SimPlatform) SecureAttention() error {
	return errors.New("Ctrl+Alt+Del needs the Windows service component (not available in the simulator)")
}

func (p *SimPlatform) ReadClipboard() (clipsync.Clip, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clip, nil
}

func (p *SimPlatform) WriteClipboard(c clipsync.Clip) error {
	p.mu.Lock()
	p.clip = c
	ws := []func(){}
	for _, w := range p.watchers {
		ws = append(ws, w)
	}
	p.mu.Unlock()
	for _, w := range ws {
		w() // like the OS, writing fires the change listener
	}
	return nil
}

// UserCopies simulates the endpoint user copying something.
func (p *SimPlatform) UserCopies(c clipsync.Clip) { _ = p.WriteClipboard(c) }

func (p *SimPlatform) WatchClipboard(fn func()) func() {
	p.mu.Lock()
	id := p.nextWatch
	p.nextWatch++
	p.watchers[id] = fn
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.watchers, id)
		p.mu.Unlock()
	}
}

func (p *SimPlatform) AskConsent(ctx context.Context, req protocol.SessionRequest, attempt int) ConsentResult {
	if p.Consent == nil {
		return ConsentResult{}
	}
	res := make(chan ConsentResult, 1)
	go func() { res <- p.Consent(req, attempt) }()
	select {
	case r := <-res:
		return r
	case <-ctx.Done():
		return ConsentResult{}
	}
}

// SimIndicator records indicator state.
type SimIndicator struct {
	mu     sync.Mutex
	Tech   string
	Perms  []string
	Status string
	Closed bool
	Hooks  IndicatorHooks
}

func (i *SimIndicator) SetPerms(p []string) { i.mu.Lock(); i.Perms = p; i.mu.Unlock() }
func (i *SimIndicator) SetStatus(s string)  { i.mu.Lock(); i.Status = s; i.mu.Unlock() }
func (i *SimIndicator) Close()              { i.mu.Lock(); i.Closed = true; i.mu.Unlock() }

// IsClosed reports whether the indicator was removed.
func (i *SimIndicator) IsClosed() bool { i.mu.Lock(); defer i.mu.Unlock(); return i.Closed }

func (p *SimPlatform) ShowIndicator(tech, email string, perms []string, hooks IndicatorHooks) Indicator {
	ind := &SimIndicator{Tech: tech + " <" + email + ">", Perms: perms, Status: "Connected", Hooks: hooks}
	p.mu.Lock()
	p.Indicators = append(p.Indicators, ind)
	p.mu.Unlock()
	return ind
}

// LastIndicator returns the most recent indicator or nil.
func (p *SimPlatform) LastIndicator() *SimIndicator {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.Indicators) == 0 {
		return nil
	}
	return p.Indicators[len(p.Indicators)-1]
}

func (p *SimPlatform) MarkReceived(path string) {
	p.mu.Lock()
	p.Received = append(p.Received, path)
	p.mu.Unlock()
}

func (p *SimPlatform) Roots() []FileEntry {
	return []FileEntry{{Name: filepath.Clean(p.FileRoot), Dir: true, Mtime: time.Now().Unix()}}
}

func (p *SimPlatform) Notify(text string) {
	p.mu.Lock()
	p.Notes = append(p.Notes, text)
	p.mu.Unlock()
}

// Protect is a no-op in the simulator; Windows uses DPAPI.
func (p *SimPlatform) Protect(b []byte) ([]byte, error)   { return b, nil }
func (p *SimPlatform) Unprotect(b []byte) ([]byte, error) { return b, nil }

// Snapshot returns copies of the recorded input.
func (p *SimPlatform) Snapshot() (keys []protocol.Key, mouse []protocol.Mouse, texts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]protocol.Key(nil), p.Keys...), append([]protocol.Mouse(nil), p.MouseEv...), append([]string(nil), p.Texts...)
}
