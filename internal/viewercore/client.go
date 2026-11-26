// Package viewercore is the platform-independent part of the technician
// viewer: launch-ticket redemption, the session connection with safe
// reconnect, screen decoding, input with held-key tracking, clipboard sync
// and the file transfer queue. The Windows UI drives it.
package viewercore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/keys"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/screen"
	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
	"github.com/TechSavage1989/remote-support-manager/internal/transfer"
)

// Launch is parsed from an rsm://connect link.
type Launch struct {
	Server    string
	SessionID string
	Ticket    string
	Pin       string
}

// ParseLink parses rsm://connect?server=...&sid=...&ticket=...[&pin=...].
func ParseLink(link string) (Launch, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "rsm" {
		return Launch{}, errors.New("not an rsm:// link")
	}
	q := u.Query()
	l := Launch{Server: q.Get("server"), SessionID: q.Get("sid"), Ticket: q.Get("ticket"), Pin: q.Get("pin")}
	su, err := url.Parse(l.Server)
	if err != nil || su.Scheme != "https" || su.Host == "" || l.SessionID == "" || l.Ticket == "" {
		return Launch{}, errors.New("the link is incomplete")
	}
	l.Server = su.Scheme + "://" + su.Host
	return l, nil
}

// RemoteFile is a row in the remote file pane.
type RemoteFile struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// Listing is a remote folder listing.
type Listing struct {
	Req     string       `json:"req"`
	Path    string       `json:"path"`
	Parent  string       `json:"parent"`
	Entries []RemoteFile `json:"entries"`
	Error   string       `json:"error"`
}

// Callbacks are invoked from the network goroutine; UIs must marshal them
// to their own thread.
type Callbacks struct {
	State    func(state, code, reason string)
	Hello    func(h protocol.Hello, resumed bool)
	Perms    func(perms []string)
	Monitors func(m protocol.Monitors)
	Frame    func(dirty image.Rectangle)
	Denied   func(d protocol.Denied)
	Notice   func(n protocol.Notice)
	Listing  func(l Listing)
	Ended    func(reason string)
	Transfer func(p transfer.Progress)
	Conflict func(c transfer.ConflictInfo, reply func(action string, applyAll bool))
	// RemoteFilesCopied offers files copied on the endpoint for download.
	RemoteFilesCopied func(paths []string)
	ApplyClipboard    func(c clipsync.Clip) error
}

// Client is one viewer session.
type Client struct {
	L     Launch
	Token string
	Info  map[string]any
	CB    Callbacks

	Xfer *transfer.Engine
	Clip *clipsync.Sync
	Held *keys.Tracker

	mu      sync.Mutex
	ws      *websocket.Conn
	wmu     sync.Mutex
	perms   protocol.Perms
	epoch   int
	state   string
	ended   bool
	canvas  screen.Canvas
	cmu     sync.Mutex
	monitor protocol.Monitors
	reqN    int
	clipOn  bool
	clipDir string

	// ReconnectWindow bounds automatic reconnect attempts.
	ReconnectWindow time.Duration
}

// Redeem exchanges the one-time ticket for a session token.
func Redeem(l Launch) (*Client, error) {
	body, _ := json.Marshal(map[string]string{"sessionId": l.SessionID, "ticket": l.Ticket})
	resp, err := tlspin.HTTPClient(l.Pin).Post(l.Server+"/api/viewer/redeem", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		msg, _ := out["error"].(string)
		return nil, fmt.Errorf("could not open the session: %s", msg)
	}
	tok, _ := out["token"].(string)
	delete(out, "token")
	c := &Client{L: l, Token: tok, Info: out, perms: protocol.Perms{}, Held: keys.NewTracker(), ReconnectWindow: 60 * time.Second, clipDir: clipsync.Both}
	c.Xfer = transfer.New(c)
	c.Xfer.ResolveLocally = true
	c.Xfer.AllowIncoming = false
	c.Clip = clipsync.New(c, true)
	return c, nil
}

// Wire connects callbacks to the engines. Call once before Run.
func (c *Client) Wire() {
	c.Xfer.OnProgress = func(p transfer.Progress) {
		if c.CB.Transfer != nil {
			c.CB.Transfer(p)
		}
	}
	c.Xfer.OnConflict = func(ci transfer.ConflictInfo, reply func(string, bool)) {
		if c.CB.Conflict != nil {
			c.CB.Conflict(ci, reply)
			return
		}
		reply(transfer.Skip, false)
	}
	c.Clip.Apply = func(cl clipsync.Clip) error {
		if c.CB.ApplyClipboard != nil {
			return c.CB.ApplyClipboard(cl)
		}
		return nil
	}
	c.Clip.OnFiles = func(paths []string) {
		if c.CB.RemoteFilesCopied != nil && c.Has(protocol.PermTransfer) {
			c.CB.RemoteFilesCopied(paths)
		}
	}
}

// Send implements the peer interfaces.
func (c *Client) Send(h any, payload []byte) error {
	b, err := protocol.Encode(h, payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws == nil {
		return errors.New("not connected")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return ws.Write(ctx, websocket.MessageBinary, b)
}

// Has reports a granted permission in the current (active) session.
func (c *Client) Has(p protocol.Permission) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == "active" && c.perms.Has(p)
}

// Perms returns the granted permissions.
func (c *Client) Perms() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.perms.List()
}

// State returns the session state.
func (c *Client) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Canvas gives the UI locked access to the decoded remote screen.
func (c *Client) Canvas(fn func(img *image.RGBA)) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	fn(c.canvas.Img)
}

// Run connects and keeps the session alive, reconnecting after drops until
// the session ends. Reconnects never revive an ended session: the server
// answers 410 once consent is withdrawn.
func (c *Client) Run(ctx context.Context) {
	c.Wire()
	var lost time.Time
	backoff := 500 * time.Millisecond
	for {
		connected, gone, err := c.once(ctx)
		if c.isEnded() || ctx.Err() != nil {
			return
		}
		if gone {
			c.finish("the session has ended")
			return
		}
		if connected {
			lost, backoff = time.Time{}, 500*time.Millisecond
		}
		if lost.IsZero() {
			lost = time.Now()
			c.suspend()
		}
		if time.Since(lost) > c.ReconnectWindow {
			c.finish("connection lost: " + errString(err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func errString(err error) string {
	if err == nil {
		return "closed"
	}
	return err.Error()
}

func (c *Client) isEnded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ended
}

func (c *Client) setState(st, code, reason string) {
	c.mu.Lock()
	c.state = st
	c.mu.Unlock()
	if c.CB.State != nil {
		c.CB.State(st, code, reason)
	}
}

func (c *Client) suspend() {
	c.ReleaseKeys()
	c.Xfer.Close("connection interrupted; use Resume when reconnected")
	c.setState("suspended", "", "reconnecting")
}

func (c *Client) finish(reason string) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	c.state = "ended"
	ws := c.ws
	c.mu.Unlock()
	c.Held.ReleaseAll() // nothing can be sent any more; clear local state
	c.Clip.Stop()
	c.Xfer.Close("session ended")
	if ws != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
	if c.CB.State != nil {
		c.CB.State("ended", "", reason)
	}
	if c.CB.Ended != nil {
		c.CB.Ended(reason)
	}
}

func (c *Client) once(ctx context.Context) (connected, gone bool, err error) {
	u, _ := url.Parse(c.L.Server)
	u.Scheme = "wss"
	u.Path = "/ws/viewer"
	u.RawQuery = url.Values{"sid": {c.L.SessionID}}.Encode()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.Token)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPClient: tlspin.HTTPClient(c.L.Pin), HTTPHeader: h})
	cancel()
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound) {
			return false, true, err
		}
		return false, false, err
	}
	ws.SetReadLimit(protocol.MaxMessage)
	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.ws == ws {
			c.ws = nil
		}
		c.mu.Unlock()
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			if cs := websocket.CloseStatus(err); cs == websocket.StatusNormalClosure || cs == websocket.StatusPolicyViolation {
				return true, true, err
			}
			return true, false, err
		}
		m, err := protocol.Decode(b)
		if err != nil {
			continue
		}
		if c.handle(m) {
			return true, true, nil
		}
	}
}

// handle returns true when the session ended.
func (c *Client) handle(m *protocol.Message) bool {
	switch m.Type {
	case protocol.TSessionState:
		var s protocol.SessionState
		if m.Into(&s) == nil {
			if s.State == "ended" {
				c.finish(s.Reason)
				return true
			}
			if s.State == "suspended" {
				c.ReleaseKeys()
				c.Xfer.Close("the endpoint disconnected; use Resume when it is back")
			}
			if s.State == "active" {
				// Consent is given, but the session is only usable after
				// session.hello once the endpoint has joined.
				if c.CB.State != nil {
					c.CB.State("joining", "", "")
				}
				break
			}
			c.setState(s.State, s.Code, s.Reason)
		}
	case protocol.TSessionHello, protocol.TSessionResumed:
		var h protocol.Hello
		if m.Into(&h) == nil {
			c.mu.Lock()
			c.perms = protocol.ParsePerms(h.Perms)
			c.epoch = h.Epoch
			c.state = "active"
			on, dir := c.clipOn, c.clipDir
			c.mu.Unlock()
			if c.CB.State != nil {
				c.CB.State("active", "", "")
			}
			// New epoch: anything queued before the reconnect is discarded.
			c.Clip.SetEpoch(h.Epoch)
			if c.perms.Has(protocol.PermClipboard) {
				_ = c.Clip.Configure(on, dir)
			} else {
				_ = c.Clip.Configure(false, dir)
			}
			_ = c.Send(map[string]string{"t": protocol.TScrRefresh}, nil)
			if c.CB.Hello != nil {
				c.CB.Hello(h, m.Type == protocol.TSessionResumed)
			}
		}
	case protocol.TSessionPerms:
		var p protocol.PermsUpdate
		if m.Into(&p) == nil {
			c.mu.Lock()
			c.perms = c.perms.Intersect(protocol.ParsePerms(p.Perms))
			now := c.perms
			c.mu.Unlock()
			if !now.Has(protocol.PermControl) {
				c.ReleaseKeys()
			}
			if !now.Has(protocol.PermTransfer) {
				c.Xfer.Close("the endpoint user withdrew file transfer permission")
			}
			if !now.Has(protocol.PermClipboard) {
				_ = c.Clip.Configure(false, clipsync.Both)
			}
			if c.CB.Perms != nil {
				c.CB.Perms(now.List())
			}
		}
	case protocol.TSessionEnd:
		var e protocol.End
		_ = m.Into(&e)
		c.finish(e.Reason)
		return true
	case protocol.TDenied:
		var d protocol.Denied
		if m.Into(&d) == nil && c.CB.Denied != nil {
			c.CB.Denied(d)
		}
	case protocol.TInNotice:
		var n protocol.Notice
		if m.Into(&n) == nil && c.CB.Notice != nil {
			c.CB.Notice(n)
		}
	case protocol.TScrMonitors:
		var mm protocol.Monitors
		if m.Into(&mm) == nil {
			c.cmu.Lock()
			c.monitor = mm
			c.cmu.Unlock()
			if c.CB.Monitors != nil {
				c.CB.Monitors(mm)
			}
		}
	case protocol.TScrFrame:
		var f protocol.Frame
		if m.Into(&f) == nil {
			c.cmu.Lock()
			dirty, err := c.canvas.Apply(f, m.Payload)
			c.cmu.Unlock()
			_ = c.Send(protocol.Ack{T: protocol.TScrAck, Seq: f.Seq}, nil)
			if err == nil && c.CB.Frame != nil {
				c.CB.Frame(dirty)
			}
		}
	case protocol.TFsResult:
		var l Listing
		if m.Into(&l) == nil && c.CB.Listing != nil {
			c.CB.Listing(l)
		}
	case protocol.TClipData, protocol.TClipFiles:
		_ = c.Clip.Handle(m)
	default:
		if strings.HasPrefix(m.Type, "xf.") {
			c.Xfer.Handle(m)
		}
	}
	return false
}

// ------------------------------------------------------------ input

// SendKey forwards a key event when control is granted.
func (c *Client) SendKey(k protocol.Key) {
	if !c.Has(protocol.PermControl) {
		return
	}
	k.T = protocol.TInKey
	c.Held.Observe(k)
	_ = c.Send(k, nil)
}

// ReleaseKeys sends key-up for everything still held (focus loss, capture
// release, disconnect) and asks the agent to release anything it injected.
func (c *Client) ReleaseKeys() {
	ups := c.Held.ReleaseAll()
	for _, k := range ups {
		_ = c.Send(k, nil)
	}
	_ = c.Send(map[string]string{"t": protocol.TInReleaseAll}, nil)
}

// SendMouse forwards a mouse event in remote monitor coordinates.
func (c *Client) SendMouse(m protocol.Mouse) {
	if !c.Has(protocol.PermControl) {
		return
	}
	m.T = protocol.TInMouse
	_ = c.Send(m, nil)
}

// PasteAsKeystrokes types text on the endpoint.
func (c *Client) PasteAsKeystrokes(text string) error {
	if !c.Has(protocol.PermControl) {
		return errors.New("control permission is not granted")
	}
	if len(text) > 64<<10 {
		return errors.New("text is too long to type (64 KB maximum)")
	}
	return c.Send(protocol.Text{T: protocol.TInText, Text: text}, nil)
}

// Combo sends a reserved key combination from the toolbar.
func (c *Client) Combo(name string) error {
	if !c.Has(protocol.PermControl) {
		return errors.New("control permission is not granted")
	}
	return c.Send(protocol.Combo{T: protocol.TInCombo, Name: name}, nil)
}

// SelectMonitor switches the remote monitor.
func (c *Client) SelectMonitor(i int) error {
	return c.Send(protocol.Select{T: protocol.TScrSelect, Index: i}, nil)
}

// Monitors returns the latest monitor list.
func (c *Client) Monitors() protocol.Monitors {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return c.monitor
}

// ------------------------------------------------------------ clipboard

// SetClipboardSync sets the sync toggle and direction.
func (c *Client) SetClipboardSync(on bool, dir string) error {
	c.mu.Lock()
	c.clipOn, c.clipDir = on, dir
	c.mu.Unlock()
	if on && !c.Has(protocol.PermClipboard) {
		return errors.New("clipboard permission is not granted")
	}
	return c.Clip.Configure(on, dir)
}

// LocalClipboardChanged is called by the UI's clipboard listener. Copied
// files are not synced; they are returned so the UI can offer them to the
// transfer queue.
func (c *Client) LocalClipboardChanged(cl clipsync.Clip) (files []string) {
	if len(cl.Files) > 0 {
		return cl.Files
	}
	if c.Has(protocol.PermClipboard) {
		_, _ = c.Clip.LocalChanged(cl)
	}
	return nil
}

// ------------------------------------------------------------ files

// ListRemote requests a folder listing ("" lists drives and known folders).
func (c *Client) ListRemote(path string) error {
	if !c.Has(protocol.PermTransfer) {
		return errors.New("file transfer permission is not granted")
	}
	c.mu.Lock()
	c.reqN++
	id := fmt.Sprint(c.reqN)
	c.mu.Unlock()
	t := protocol.TFsList
	if path == "" {
		t = protocol.TFsRoots
	}
	return c.Send(map[string]string{"t": t, "req": id, "path": path}, nil)
}

// Upload sends local files and folders to a remote folder.
func (c *Client) Upload(local []string, remoteDir string) (string, error) {
	if !c.Has(protocol.PermTransfer) {
		return "", errors.New("file transfer permission is not granted")
	}
	return c.Xfer.Upload(local, remoteDir)
}

// Download fetches remote files and folders into a local folder.
func (c *Client) Download(remote []string, localDir string) (string, error) {
	if !c.Has(protocol.PermTransfer) {
		return "", errors.New("file transfer permission is not granted")
	}
	return c.Xfer.Pull(remote, localDir)
}

// Resume re-runs an interrupted job, reusing verified partial data.
func (c *Client) Resume(jobID string) (string, error) {
	if !c.Has(protocol.PermTransfer) {
		return "", errors.New("file transfer permission is not granted")
	}
	return c.Xfer.Retry(jobID)
}

// End ends the session for both sides.
func (c *Client) End() {
	c.ReleaseKeys()
	_ = c.Send(protocol.End{T: protocol.TSessionEnd, Reason: "ended by the technician"}, nil)
	c.finish("you ended the session")
}

// DropConnection closes the network connection without ending the session,
// exactly like a network failure. It exists for automated reconnect tests
// and the viewer's hidden diagnostics.
func (c *Client) DropConnection() {
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws != nil {
		_ = ws.CloseNow()
	}
}
