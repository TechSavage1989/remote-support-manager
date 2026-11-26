package agentcore

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/keys"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/screen"
	"github.com/TechSavage1989/remote-support-manager/internal/transfer"
)

// ReconnectWindow is how long the agent keeps trying after an unexpected
// drop. It matches the server's default grace period.
var ReconnectWindow = 60 * time.Second

// FrameInterval limits capture rate.
var FrameInterval = 80 * time.Millisecond

// Session is one consented support session on the endpoint.
type Session struct {
	a  *Agent
	id string

	mu       sync.Mutex
	ws       *websocket.Conn
	wmu      sync.Mutex
	perms    protocol.Perms
	epoch    int
	monitor  int
	unacked  int
	ended    bool
	endWhy   string
	active   bool
	ind      Indicator
	techName string
	done     chan struct{}
	doneOnce sync.Once

	enc    screen.Encoder
	xfer   *transfer.Engine
	clip   *clipsync.Sync
	held   *keys.Tracker
	stopCB func()
	kick   chan struct{}
}

func newSession(a *Agent, id string) *Session {
	s := &Session{a: a, id: id, perms: protocol.Perms{}, done: make(chan struct{}), held: keys.NewTracker(), kick: make(chan struct{}, 1)}
	s.enc.Quality = 70
	s.xfer = transfer.New(s)
	s.xfer.AllowIncoming = true
	s.xfer.MarkReceived = a.P.MarkReceived
	s.clip = clipsync.New(s, false)
	s.clip.Apply = a.P.WriteClipboard
	return s
}

// Send implements the transfer and clipboard peer interfaces.
func (s *Session) Send(h any, payload []byte) error {
	b, err := protocol.Encode(h, payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	ws := s.ws
	s.mu.Unlock()
	if ws == nil {
		return errors.New("not connected")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return ws.Write(ctx, websocket.MessageBinary, b)
}

func (s *Session) has(p protocol.Permission) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && s.perms.Has(p)
}

func (s *Session) stop(why string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended, s.endWhy, s.active = true, why, false
	ws := s.ws
	ind := s.ind
	s.mu.Unlock()
	s.releaseKeys()
	s.clip.Stop()
	s.xfer.Close("session ended")
	if s.stopCB != nil {
		s.stopCB()
	}
	if ind != nil {
		ind.Close()
	}
	if ws != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "session ended")
	}
	s.doneOnce.Do(func() { close(s.done) })
	s.a.P.Notify("Remote support session ended: " + why)
}

func (s *Session) releaseKeys() {
	for _, k := range s.held.ReleaseAll() {
		_ = s.a.P.InjectKey(k)
	}
}

func (s *Session) run(ctx context.Context) {
	s.stopCB = s.a.P.WatchClipboard(func() {
		if !s.has(protocol.PermClipboard) {
			return
		}
		c, err := s.a.P.ReadClipboard()
		if err == nil {
			_, _ = s.clip.LocalChanged(c)
		}
	})
	go s.captureLoop()
	var lost time.Time
	backoff := 500 * time.Millisecond
	for {
		connected, err := s.connectOnce(ctx)
		if err != nil {
			s.a.logf("session %s: %v", s.id, err)
		}
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		if ended || ctx.Err() != nil {
			s.stop("session finished")
			return
		}
		var gone *goneErr
		if errors.As(err, &gone) {
			s.stop("the session was ended")
			return
		}
		if connected {
			lost = time.Time{}
			backoff = 500 * time.Millisecond
		}
		if lost.IsZero() {
			lost = time.Now()
			s.onSuspend()
		}
		if time.Since(lost) > ReconnectWindow {
			s.stop("connection lost")
			return
		}
		select {
		case <-ctx.Done():
			s.stop("agent stopped")
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

type goneErr struct{}

func (goneErr) Error() string { return "session gone" }

func (s *Session) onSuspend() {
	s.mu.Lock()
	s.active = false
	ind := s.ind
	s.mu.Unlock()
	s.releaseKeys()
	s.xfer.Close("connection interrupted")
	if ind != nil {
		ind.SetStatus("Reconnecting…")
	}
}

func (s *Session) connectOnce(ctx context.Context) (bool, error) {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := s.a.dial(dctx, "/ws/agent/session?sid="+s.id)
	cancel()
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized) {
			return false, &goneErr{}
		}
		return false, err
	}
	s.mu.Lock()
	s.ws = ws
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.ws == ws {
			s.ws = nil
		}
		s.mu.Unlock()
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			s.mu.Lock()
			ended := s.ended
			s.mu.Unlock()
			if ended {
				return true, nil
			}
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
				return true, &goneErr{}
			}
			return true, err
		}
		m, err := protocol.Decode(b)
		if err != nil {
			continue
		}
		if err := s.handle(m); err != nil {
			return true, err
		}
	}
}

func (s *Session) handle(m *protocol.Message) error {
	switch m.Type {
	case protocol.TSessionHello, protocol.TSessionResumed:
		var h protocol.Hello
		if err := m.Into(&h); err != nil {
			return err
		}
		s.onHello(h)
		return nil
	case protocol.TSessionEnd:
		var e protocol.End
		_ = m.Into(&e)
		s.stop(e.Reason)
		return &goneErr{}
	case protocol.TSessionState:
		var st protocol.SessionState
		if m.Into(&st) == nil && st.State == "suspended" {
			s.onSuspend()
		}
		return nil
	case protocol.TDenied, protocol.TPong:
		return nil
	}
	need, ok := protocol.Class(m.Type)
	if !ok || (need != "" && !s.has(need)) {
		return nil // the server already filters; this is defence in depth
	}
	switch m.Type {
	case protocol.TScrSelect:
		var sel protocol.Select
		if m.Into(&sel) == nil {
			s.mu.Lock()
			if sel.Index >= 0 && sel.Index < len(s.a.P.Monitors()) {
				s.monitor = sel.Index
			}
			s.unacked = 0
			s.mu.Unlock()
			s.enc.Reset()
			s.sendMonitors()
			s.poke()
		}
	case protocol.TScrAck:
		s.mu.Lock()
		if s.unacked > 0 {
			s.unacked--
		}
		s.mu.Unlock()
		s.poke()
	case protocol.TScrRefresh:
		s.mu.Lock()
		s.unacked = 0
		s.mu.Unlock()
		s.enc.Reset()
		s.poke()
	case protocol.TInMouse:
		var mm protocol.Mouse
		if m.Into(&mm) == nil {
			_ = s.a.P.InjectMouse(mm, s.currentMonitor())
		}
	case protocol.TInKey:
		var k protocol.Key
		if m.Into(&k) == nil {
			s.held.Observe(k)
			_ = s.a.P.InjectKey(k)
		}
	case protocol.TInText:
		var t protocol.Text
		if m.Into(&t) == nil && len(t.Text) <= 64<<10 {
			_ = s.a.P.InjectText(t.Text)
		}
	case protocol.TInCombo:
		var c protocol.Combo
		if m.Into(&c) == nil {
			s.combo(c.Name)
		}
	case protocol.TInReleaseAll:
		s.releaseKeys()
	case protocol.TClipData, protocol.TClipConfig, protocol.TClipFiles:
		_ = s.clip.Handle(m)
	case protocol.TFsList, protocol.TFsRoots:
		s.fsList(m)
	default:
		if len(m.Type) > 3 && m.Type[:3] == "xf." {
			s.xfer.Handle(m)
		}
	}
	return nil
}

func (s *Session) combo(name string) {
	if name == "ctrl-alt-del" {
		if err := s.a.P.SecureAttention(); err != nil {
			_ = s.Send(protocol.Notice{T: protocol.TInNotice, About: "ctrl-alt-del", Text: err.Error()}, nil)
		}
		return
	}
	ev, ok := keys.ComboEvents(name)
	if !ok {
		return
	}
	for _, k := range ev {
		_ = s.a.P.InjectKey(k)
		time.Sleep(15 * time.Millisecond)
	}
}

func (s *Session) currentMonitor() protocol.Monitor {
	mons := s.a.P.Monitors()
	s.mu.Lock()
	i := s.monitor
	s.mu.Unlock()
	if i < 0 || i >= len(mons) {
		i = 0
	}
	if len(mons) == 0 {
		return protocol.Monitor{}
	}
	return mons[i]
}

func (s *Session) sendMonitors() {
	s.mu.Lock()
	cur := s.monitor
	s.mu.Unlock()
	_ = s.Send(protocol.Monitors{T: protocol.TScrMonitors, Monitors: s.a.P.Monitors(), Current: cur}, nil)
}

func (s *Session) onHello(h protocol.Hello) {
	s.mu.Lock()
	first := s.ind == nil
	s.perms = protocol.ParsePerms(h.Perms)
	s.epoch = h.Epoch
	s.active = true
	s.unacked = 0
	s.techName = h.Technician
	s.mu.Unlock()
	s.clip.SetEpoch(h.Epoch)
	s.enc.Reset()
	if first {
		ind := s.a.P.ShowIndicator(h.Technician, h.TechEmail, h.Perms, IndicatorHooks{
			End: func() {
				_ = s.Send(protocol.End{T: protocol.TSessionEnd, Reason: "ended by the endpoint user"}, nil)
				s.stop("you ended the session")
			},
			ChangePerms: func(p []string) {
				s.mu.Lock()
				s.perms = s.perms.Intersect(protocol.ParsePerms(p))
				list := s.perms.List()
				s.mu.Unlock()
				if !protocol.ParsePerms(list).Has(protocol.PermControl) {
					s.releaseKeys()
				}
				if !protocol.ParsePerms(list).Has(protocol.PermTransfer) {
					s.xfer.Close("file transfer permission was withdrawn")
				}
				if !protocol.ParsePerms(list).Has(protocol.PermClipboard) {
					_ = s.clip.Configure(false, clipsync.Both)
				}
				_ = s.Send(protocol.PermsUpdate{T: protocol.TSessionPerms, Perms: list}, nil)
			},
		})
		s.mu.Lock()
		s.ind = ind
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		ind := s.ind
		s.mu.Unlock()
		ind.SetPerms(h.Perms)
		ind.SetStatus("Connected")
	}
	s.sendMonitors()
	s.poke()
}

func (s *Session) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Session) captureLoop() {
	t := time.NewTicker(FrameInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		case <-s.kick:
		}
		s.mu.Lock()
		ok := s.active && s.perms.Has(protocol.PermView) && s.ws != nil && s.unacked < 2
		mi := s.monitor
		s.mu.Unlock()
		if !ok {
			continue
		}
		mon := s.currentMonitor()
		img, err := s.a.P.Capture(mon)
		if err != nil {
			continue
		}
		hdr, payload, changed, err := s.enc.Encode(img, mi, false)
		if err != nil || !changed {
			continue
		}
		s.mu.Lock()
		s.unacked++
		s.mu.Unlock()
		if s.Send(hdr, payload) != nil {
			s.enc.Reset()
		}
	}
}

// ---------------------------------------------------------------- file listing

type fsReq struct {
	T    string `json:"t"`
	Req  string `json:"req"`
	Path string `json:"path"`
}

type fsResult struct {
	T       string      `json:"t"`
	Req     string      `json:"req"`
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []FileEntry `json:"entries"`
	Error   string      `json:"error,omitempty"`
}

func (s *Session) fsList(m *protocol.Message) {
	var r fsReq
	if m.Into(&r) != nil {
		return
	}
	res := fsResult{T: protocol.TFsResult, Req: r.Req, Path: r.Path, Entries: []FileEntry{}}
	if m.Type == protocol.TFsRoots || r.Path == "" {
		res.Path = ""
		res.Entries = s.a.P.Roots()
		_ = s.Send(res, nil)
		return
	}
	p := filepath.Clean(r.Path)
	res.Path = p
	if parent := filepath.Dir(p); parent != p {
		res.Parent = parent
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		res.Error = "cannot open folder"
		_ = s.Send(res, nil)
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		res.Entries = append(res.Entries, FileEntry{Name: e.Name(), Dir: e.IsDir(), Size: info.Size(), Mtime: info.ModTime().Unix()})
		if len(res.Entries) >= 5000 {
			break
		}
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		if res.Entries[i].Dir != res.Entries[j].Dir {
			return res.Entries[i].Dir
		}
		return res.Entries[i].Name < res.Entries[j].Name
	})
	_ = s.Send(res, nil)
}
