// Package agentcore is the platform-independent part of the endpoint agent:
// enrollment, the outbound control channel, consent, and the session data
// channel. Windows specifics live behind the Platform interface.
package agentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
)

// Version is set at build time.
var Version = "0.1.0-dev"

// Disk mirrors the server inventory type.
type Disk struct {
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Inventory is reported to the server.
type Inventory struct {
	Hostname     string `json:"hostname"`
	OSName       string `json:"osName"`
	OSVersion    string `json:"osVersion"`
	UptimeSec    int64  `json:"uptimeSec"`
	Disks        []Disk `json:"disks"`
	AgentVersion string `json:"agentVersion"`
	LoggedOnUser string `json:"loggedOnUser"`
}

// ConsentResult is the endpoint user's answer.
type ConsentResult struct {
	Accept bool
	Code   string
	Perms  []string
}

// Indicator is the always-visible session notice on the endpoint.
type Indicator interface {
	SetPerms(perms []string)
	SetStatus(text string)
	Close()
}

// IndicatorHooks are the endpoint user's controls.
type IndicatorHooks struct {
	End         func()
	ChangePerms func(perms []string)
}

// FileEntry is one directory listing row.
type FileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// Platform is implemented for Windows and by the simulator.
type Platform interface {
	Inventory() Inventory
	Monitors() []protocol.Monitor
	Capture(mon protocol.Monitor) (*image.RGBA, error)
	InjectMouse(m protocol.Mouse, mon protocol.Monitor) error
	InjectKey(k protocol.Key) error
	InjectText(text string) error
	// SecureAttention sends Ctrl+Alt+Del through the supported Windows API.
	// It returns an error explaining why it is unavailable otherwise.
	SecureAttention() error
	ReadClipboard() (clipsync.Clip, error)
	WriteClipboard(c clipsync.Clip) error
	// WatchClipboard calls fn on every local clipboard change until stop.
	WatchClipboard(fn func()) (stop func())
	// AskConsent shows the consent prompt. It must return when ctx ends.
	AskConsent(ctx context.Context, req protocol.SessionRequest, attempt int) ConsentResult
	ShowIndicator(technician, email string, perms []string, hooks IndicatorHooks) Indicator
	MarkReceived(path string)
	Roots() []FileEntry
	Notify(text string)
	// Protect/Unprotect encrypt the stored device secret for this user.
	Protect(b []byte) ([]byte, error)
	Unprotect(b []byte) ([]byte, error)
}

// Config is stored in the user's app data folder.
type Config struct {
	Server   string `json:"server"`
	Pin      string `json:"pin"`
	DeviceID string `json:"deviceId"`
	Secret   []byte `json:"secret"` // protected by Platform.Protect
}

// ConfigPath returns the agent's settings file.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dir = v
	}
	return filepath.Join(dir, "RemoteSupportManager", "agent.json"), nil
}

// LoadConfig reads the configuration from path.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	return &c, json.Unmarshal(b, &c)
}

// Save writes the configuration (owner-only permissions).
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Enroll exchanges a one-time token for device credentials.
func Enroll(p Platform, serverURL, pin, token, cfgPath string) (*Config, error) {
	u, err := url.Parse(strings.TrimRight(serverURL, "/"))
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("server must be an https:// URL")
	}
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"token": token, "hostname": host})
	resp, err := tlspin.HTTPClient(pin).Post(u.String()+"/api/agent/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		DeviceID string `json:"deviceId"`
		Secret   string `json:"secret"`
		Error    string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("enrollment refused: %s", out.Error)
	}
	sec, err := p.Protect([]byte(out.Secret))
	if err != nil {
		return nil, err
	}
	c := &Config{Server: u.String(), Pin: tlspin.Normalize(pin), DeviceID: out.DeviceID, Secret: sec}
	return c, c.Save(cfgPath)
}

// Agent runs the control channel.
type Agent struct {
	P       Platform
	Cfg     *Config
	CfgPath string
	Log     *log.Logger

	// InventoryEvery controls heartbeat frequency (default 60 s).
	InventoryEvery time.Duration

	mu       sync.Mutex
	consents map[string]context.CancelFunc
	sessions map[string]*Session
	secret   string
}

// ErrRevoked is returned by Run when the server revoked this device.
var ErrRevoked = errors.New("this device's access was revoked by an administrator")

func (a *Agent) logf(f string, v ...any) {
	if a.Log != nil {
		a.Log.Printf(f, v...)
	}
}

func (a *Agent) wsURL(path string) string {
	u, _ := url.Parse(a.Cfg.Server)
	u.Scheme = "wss"
	p, q, _ := strings.Cut(path, "?")
	u.Path = p
	u.RawQuery = q
	return u.String()
}

func (a *Agent) dial(ctx context.Context, path string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+a.Cfg.DeviceID+"."+a.secret)
	c, resp, err := websocket.Dial(ctx, a.wsURL(path), &websocket.DialOptions{HTTPClient: tlspin.HTTPClient(a.Cfg.Pin), HTTPHeader: h})
	if err == nil {
		c.SetReadLimit(protocol.MaxMessage)
	}
	return c, resp, err
}

// Run keeps the outbound control channel open until ctx ends or the device
// is revoked. It reconnects with jittered exponential backoff.
func (a *Agent) Run(ctx context.Context) error {
	sec, err := a.P.Unprotect(a.Cfg.Secret)
	if err != nil {
		return fmt.Errorf("cannot read device credential: %w", err)
	}
	a.secret = string(sec)
	a.consents = map[string]context.CancelFunc{}
	a.sessions = map[string]*Session{}
	if a.InventoryEvery == 0 {
		a.InventoryEvery = 60 * time.Second
	}
	backoff := time.Second
	for {
		err := a.controlOnce(ctx)
		if errors.Is(err, ErrRevoked) {
			a.forget()
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.logf("control channel: %v; retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + time.Duration(rand.Int63n(int64(backoff/2)+1))):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// Busy reports whether a support request or session is in progress. The
// updater never installs while Busy is true.
func (a *Agent) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions) > 0 || len(a.consents) > 0
}

func (a *Agent) forget() {
	a.mu.Lock()
	for _, s := range a.sessions {
		s.stop("device access was revoked")
	}
	a.mu.Unlock()
	if a.CfgPath != "" {
		_ = os.Remove(a.CfgPath)
	}
	a.P.Notify("Remote support access for this PC was revoked by an administrator.")
}

func (a *Agent) controlOnce(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := a.dial(dctx, "/ws/agent")
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == 401 {
			// The credential is no longer valid: treat as revoked.
			return ErrRevoked
		}
		return err
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	a.logf("connected to %s", a.Cfg.Server)
	var wmu sync.Mutex
	send := func(h any) error {
		b, err := protocol.Encode(h, nil)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return ws.Write(wctx, websocket.MessageBinary, b)
	}
	sendInv := func() error {
		inv := a.P.Inventory()
		inv.AgentVersion = Version
		m := struct {
			T string `json:"t"`
			Inventory
		}{protocol.TAgentInventory, inv}
		return send(m)
	}
	if err := sendInv(); err != nil {
		return err
	}
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	go func() {
		t := time.NewTicker(a.InventoryEvery)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				if sendInv() != nil {
					_ = ws.Close(websocket.StatusGoingAway, "heartbeat failed")
					return
				}
			}
		}
	}()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		m, err := protocol.Decode(b)
		if err != nil {
			continue
		}
		switch m.Type {
		case protocol.TDeviceRevoked:
			return ErrRevoked
		case protocol.TSessionRequest:
			var req protocol.SessionRequest
			if m.Into(&req) == nil {
				a.consent(cctx, req, send)
			}
		case protocol.TSessionCancel:
			var c protocol.SessionCancel
			if m.Into(&c) == nil {
				a.mu.Lock()
				if cancel := a.consents[c.SessionID]; cancel != nil {
					cancel()
				}
				a.mu.Unlock()
				a.P.Notify("The support request was withdrawn: " + c.Reason)
			}
		case protocol.TSessionStart:
			var st protocol.SessionStart
			if m.Into(&st) == nil {
				a.startSession(ctx, st.SessionID)
			}
		}
	}
}

func (a *Agent) consent(ctx context.Context, req protocol.SessionRequest, send func(any) error) {
	a.mu.Lock()
	attempt := 1
	if prev := a.consents[req.SessionID]; prev != nil {
		prev()
		attempt = 2 // a repeated request means the code was wrong
	}
	cctx, cancel := context.WithDeadline(ctx, time.Unix(req.ExpiresAt, 0))
	a.consents[req.SessionID] = cancel
	a.mu.Unlock()
	go func() {
		defer cancel()
		res := a.P.AskConsent(cctx, req, attempt)
		if cctx.Err() != nil && !res.Accept {
			return // withdrawn or expired
		}
		_ = send(protocol.SessionConsent{T: protocol.TSessionConsent, SessionID: req.SessionID, Accept: res.Accept, Code: res.Code, Perms: res.Perms})
	}()
}

func (a *Agent) startSession(ctx context.Context, id string) {
	a.mu.Lock()
	if a.sessions[id] != nil {
		a.mu.Unlock()
		return
	}
	s := newSession(a, id)
	a.sessions[id] = s
	a.mu.Unlock()
	go func() {
		s.run(ctx)
		a.mu.Lock()
		delete(a.sessions, id)
		a.mu.Unlock()
	}()
}
