// Package protocol defines the wire format shared by the server, the Windows
// agent and the technician viewer.
//
// Every WebSocket message is one binary frame:
//
//	[4-byte big-endian header length][JSON header][optional binary payload]
//
// The JSON header always carries "t" (the message type). The server relays
// session traffic between viewer and agent and decides whether a message is
// allowed by looking only at "t" (see Class), so payloads such as screen
// tiles, file chunks and clipboard data are never parsed or logged by it.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxHeader bounds the JSON header so a peer cannot force huge allocations.
const MaxHeader = 1 << 20

// MaxMessage bounds a whole frame (header + payload).
const MaxMessage = 8 << 20

// Message is a decoded frame.
type Message struct {
	Type    string
	Header  json.RawMessage
	Payload []byte
}

type typeOnly struct {
	T string `json:"t"`
}

// Encode marshals hdr (which must contain a "t" field) and appends payload.
func Encode(hdr any, payload []byte) ([]byte, error) {
	h, err := json.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	if len(h) > MaxHeader {
		return nil, errors.New("protocol: header too large")
	}
	out := make([]byte, 4+len(h)+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(h)))
	copy(out[4:], h)
	copy(out[4+len(h):], payload)
	return out, nil
}

// MustEncode is Encode for headers that cannot fail to marshal.
func MustEncode(hdr any, payload []byte) []byte {
	b, err := Encode(hdr, payload)
	if err != nil {
		panic(err)
	}
	return b
}

// Decode splits a frame into its parts.
func Decode(frame []byte) (*Message, error) {
	if len(frame) < 4 {
		return nil, errors.New("protocol: short frame")
	}
	if len(frame) > MaxMessage {
		return nil, errors.New("protocol: frame too large")
	}
	n := binary.BigEndian.Uint32(frame)
	if n > MaxHeader || int(n) > len(frame)-4 {
		return nil, errors.New("protocol: bad header length")
	}
	h := frame[4 : 4+n]
	var t typeOnly
	if err := json.Unmarshal(h, &t); err != nil {
		return nil, fmt.Errorf("protocol: header: %w", err)
	}
	if t.T == "" {
		return nil, errors.New("protocol: missing type")
	}
	return &Message{Type: t.T, Header: json.RawMessage(h), Payload: frame[4+n:]}, nil
}

// Into unmarshals the header into v.
func (m *Message) Into(v any) error { return json.Unmarshal(m.Header, v) }

// Permission is one independently grantable session right.
type Permission string

const (
	PermView      Permission = "view"
	PermControl   Permission = "control"
	PermClipboard Permission = "clipboard"
	PermTransfer  Permission = "transfer"
)

// AllPermissions in display order.
var AllPermissions = []Permission{PermView, PermControl, PermClipboard, PermTransfer}

// Perms is a set of permissions.
type Perms map[Permission]bool

// ParsePerms builds a set from a list, ignoring unknown values.
func ParsePerms(list []string) Perms {
	p := Perms{}
	for _, s := range list {
		for _, k := range AllPermissions {
			if string(k) == s {
				p[k] = true
			}
		}
	}
	return p
}

// List returns the set in display order.
func (p Perms) List() []string {
	out := []string{}
	for _, k := range AllPermissions {
		if p[k] {
			out = append(out, string(k))
		}
	}
	return out
}

// Intersect returns permissions present in both sets.
func (p Perms) Intersect(o Perms) Perms {
	r := Perms{}
	for k, v := range p {
		if v && o[k] {
			r[k] = true
		}
	}
	return r
}

// Has reports whether k is granted.
func (p Perms) Has(k Permission) bool { return p[k] }

// Session message types. The prefix decides the permission class.
const (
	// session control (always allowed inside an authorised session)
	TSessionHello   = "session.hello"   // server -> both: epoch, perms, technician, device
	TSessionPerms   = "session.perms"   // agent -> server -> viewer: endpoint user changed perms
	TSessionEnd     = "session.end"     // any -> any: reason
	TSessionResumed = "session.resumed" // server -> both after reconnect
	TPing           = "ping"
	TPong           = "pong"
	TDenied         = "denied"        // server -> sender: message blocked by permissions
	TSessionState   = "session.state" // server -> viewer: pending/active/ended + code

	// agent control channel (not relayed)
	TAgentInventory = "agent.inventory"
	TSessionRequest = "session.request" // server -> agent: ask the endpoint user
	TSessionCancel  = "session.cancel"  // server -> agent: request withdrawn/expired
	TSessionConsent = "session.consent" // agent -> server: user's answer
	TSessionStart   = "session.start"   // server -> agent: open the data channel
	TDeviceRevoked  = "device.revoked"  // server -> agent: forget credentials
	TDeviceRemove   = "device.remove"   // server -> agent: session deleted or access removed; uninstall

	// view
	TScrMonitors = "scr.monitors" // agent -> viewer: list
	TScrSelect   = "scr.select"   // viewer -> agent: monitor index
	TScrFrame    = "scr.frame"    // agent -> viewer: tiles
	TScrAck      = "scr.ack"      // viewer -> agent: flow control
	TScrRefresh  = "scr.refresh"  // viewer -> agent: request full frame

	// control
	TInMouse      = "in.mouse"
	TInKey        = "in.key"
	TInText       = "in.text"  // paste as keystrokes
	TInCombo      = "in.combo" // reserved key combos (toolbar)
	TInReleaseAll = "in.release_all"
	TInNotice     = "in.notice" // agent -> viewer: an input action could not be done

	// clipboard
	TClipData   = "clip.data"
	TClipFiles  = "clip.files" // list of copied paths offered for transfer (names only)
	TClipConfig = "clip.config"

	// transfer
	TFsRoots    = "fs.roots"
	TFsList     = "fs.list"
	TFsResult   = "fs.result"
	TXfBegin    = "xf.begin"
	TXfPull     = "xf.pull"
	TXfFile     = "xf.file"
	TXfConflict = "xf.conflict"
	TXfResolve  = "xf.resolve"
	TXfReady    = "xf.ready"
	TXfChunk    = "xf.chunk"
	TXfAck      = "xf.ack"
	TXfEnd      = "xf.end"
	TXfDone     = "xf.done"
	TXfCancel   = "xf.cancel"
	TXfError    = "xf.error"
	TXfComplete = "xf.complete"
	TXfMkdir    = "xf.mkdir"
	TXfManifest = "xf.manifest"
)

// Class returns the permission a message type needs, or "" for session
// control messages that every authorised participant may send. ok is false
// for unknown types, which the server drops.
func Class(t string) (p Permission, ok bool) {
	switch t {
	case TSessionHello, TSessionPerms, TSessionEnd, TSessionResumed, TPing, TPong, TDenied, TSessionState:
		return "", true
	}
	prefix := t
	for i := 0; i < len(t); i++ {
		if t[i] == '.' {
			prefix = t[:i]
			break
		}
	}
	switch prefix {
	case "scr":
		return PermView, true
	case "in":
		return PermControl, true
	case "clip":
		return PermClipboard, true
	case "fs", "xf":
		return PermTransfer, true
	}
	return "", false
}

// Common headers.

type Hello struct {
	T          string   `json:"t"`
	SessionID  string   `json:"sessionId"`
	Epoch      int      `json:"epoch"`
	Perms      []string `json:"perms"`
	Technician string   `json:"technician"`
	TechEmail  string   `json:"techEmail"`
	Device     string   `json:"device"`
}

type PermsUpdate struct {
	T     string   `json:"t"`
	Perms []string `json:"perms"`
}

type End struct {
	T      string `json:"t"`
	Reason string `json:"reason"`
}

type Denied struct {
	T       string `json:"t"`
	Blocked string `json:"blocked"`
	Need    string `json:"need"`
}

type Monitor struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Primary bool   `json:"primary"`
}

type Monitors struct {
	T        string    `json:"t"`
	Monitors []Monitor `json:"monitors"`
	Current  int       `json:"current"`
}

type Select struct {
	T     string `json:"t"`
	Index int    `json:"index"`
}

type Tile struct {
	X   int `json:"x"`
	Y   int `json:"y"`
	W   int `json:"w"`
	H   int `json:"h"`
	Len int `json:"len"`
}

type Frame struct {
	T       string `json:"t"`
	Seq     int64  `json:"seq"`
	Monitor int    `json:"monitor"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Full    bool   `json:"full"`
	Tiles   []Tile `json:"tiles"`
}

type Ack struct {
	T   string `json:"t"`
	Seq int64  `json:"seq"`
}

type Mouse struct {
	T      string `json:"t"`
	X      int    `json:"x"` // pixels within the selected monitor
	Y      int    `json:"y"`
	Action string `json:"action"` // move, down, up, wheel, hwheel
	Button string `json:"button,omitempty"`
	Delta  int    `json:"delta,omitempty"`
}

type Key struct {
	T    string `json:"t"`
	Scan uint16 `json:"scan"`
	Ext  bool   `json:"ext"`
	VK   uint16 `json:"vk"`
	Down bool   `json:"down"`
}

type Text struct {
	T    string `json:"t"`
	Text string `json:"text"`
}

type Combo struct {
	T    string `json:"t"`
	Name string `json:"name"`
}

// Combo names supported by the toolbar.
var Combos = []string{"ctrl-alt-del", "ctrl-shift-esc", "alt-tab", "win", "win-r", "win-l", "alt-f4", "print-screen"}

type ClipFormat struct {
	F   string `json:"f"` // text, rtf, html, png
	Len int    `json:"len"`
}

type ClipData struct {
	T       string       `json:"t"`
	Epoch   int          `json:"epoch"`
	Seq     int64        `json:"seq"`
	Hash    string       `json:"hash"`
	Formats []ClipFormat `json:"formats"`
}

type ClipFiles struct {
	T     string   `json:"t"`
	Epoch int      `json:"epoch"`
	Paths []string `json:"paths"`
}

type ClipConfig struct {
	T         string `json:"t"`
	Enabled   bool   `json:"enabled"`
	Direction string `json:"direction"` // both, to-remote, to-local
}

type SessionState struct {
	T      string `json:"t"`
	State  string `json:"state"` // pending, active, suspended, ended
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type SessionRequest struct {
	T          string   `json:"t"`
	SessionID  string   `json:"sessionId"`
	Technician string   `json:"technician"`
	TechEmail  string   `json:"techEmail"`
	Org        string   `json:"org"`
	Perms      []string `json:"perms"`
	ExpiresAt  int64    `json:"expiresAt"`
}

type SessionConsent struct {
	T         string   `json:"t"`
	SessionID string   `json:"sessionId"`
	Accept    bool     `json:"accept"`
	Code      string   `json:"code"`
	Perms     []string `json:"perms"`
}

type SessionStart struct {
	T         string `json:"t"`
	SessionID string `json:"sessionId"`
	// Standing is true when the endpoint gave lasting consent (one-time,
	// support or unattended clients) and no prompt was shown for this
	// connection. The session bar is still shown.
	Standing   bool   `json:"standing,omitempty"`
	Technician string `json:"technician,omitempty"`
	TechEmail  string `json:"techEmail,omitempty"`
}

// DeviceRemove tells a client that its session was deleted or its access
// removed. The client stops and uninstalls itself.
type DeviceRemove struct {
	T      string `json:"t"`
	Reason string `json:"reason"`
}

// Client kinds. Attended clients ask for consent and a code on every
// session. The other kinds record the endpoint's consent once, when the
// client is run or installed, and keep showing the session bar.
const (
	KindAttended = "attended" // installed agent, consent + code each time
	KindOneTime  = "onetime"  // portable, nothing installed, gone after one session
	KindSupport  = "support"  // installed per user, stays until the technician deletes the session
	KindAccess   = "access"   // unattended service, installed by an administrator
)

// ValidKind reports whether k is a known client kind.
func ValidKind(k string) bool {
	return k == KindAttended || k == KindOneTime || k == KindSupport || k == KindAccess
}

type SessionCancel struct {
	T         string `json:"t"`
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason"`
}

type Notice struct {
	T     string `json:"t"`
	About string `json:"about"`
	Text  string `json:"text"`
}

// ClientFile is client.json, delivered next to RemoteSupportAgent.exe in
// a download from the console. It names the server, its certificate pin
// and (except for the generic one-time client) the invitation.
type ClientFile struct {
	Server string `json:"server"`
	Pin    string `json:"pin"`
	Org    string `json:"org"`
	Invite string `json:"invite,omitempty"`
	Kind   string `json:"kind,omitempty"`
}
