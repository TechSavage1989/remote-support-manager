package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// conn is one WebSocket with a dedicated writer.
type conn struct {
	ws     *websocket.Conn
	out    chan []byte
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func newConn(parent context.Context, ws *websocket.Conn) *conn {
	ctx, cancel := context.WithCancel(parent)
	c := &conn{ws: ws, out: make(chan []byte, 64), ctx: ctx, cancel: cancel}
	ws.SetReadLimit(protocol.MaxMessage)
	go c.writer()
	return c
}

func (c *conn) writer() {
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			wctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
			err := c.ws.Write(wctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "write failed")
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

// send queues a frame; it blocks (back-pressure) until queued or closed.
func (c *conn) send(b []byte) bool {
	select {
	case c.out <- b:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *conn) sendMsg(h any, payload []byte) bool {
	b, err := protocol.Encode(h, payload)
	if err != nil {
		return false
	}
	return c.send(b)
}

func (c *conn) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		// Give queued frames (such as session.end) a moment to flush.
		go func() {
			deadline := time.After(2 * time.Second)
			for len(c.out) > 0 {
				select {
				case <-deadline:
					goto done
				case <-time.After(20 * time.Millisecond):
				}
			}
		done:
			c.cancel()
			_ = c.ws.Close(code, reason)
		}()
	})
}

// read returns the next decoded message.
func (c *conn) read() (*protocol.Message, error) {
	typ, b, err := c.ws.Read(c.ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, errors.New("binary frames only")
	}
	return protocol.Decode(b)
}

// agentLink is a device's control channel.
type agentLink struct {
	deviceID string
	clientID int64
	c        *conn
}

// Session states.
const (
	statePending   = "pending"
	stateActive    = "active"
	stateSuspended = "suspended"
	stateEnded     = "ended"
)

type liveSession struct {
	mu sync.Mutex

	id         string
	deviceID   string
	deviceName string
	clientID   int64
	userID     int64
	techName   string
	techEmail  string

	requested protocol.Perms
	granted   protocol.Perms
	code      string
	codeTries int
	expires   time.Time

	state       string
	endReason   string
	epoch       int
	viewerToken string // hash
	ticket      string // hash of one-time launch ticket
	ticketExp   time.Time
	ticketUsed  bool

	viewer, agent         *conn
	viewerLost, agentLost time.Time

	denied     map[string]bool
	clipCounts map[string]int
	stateSubs  []chan struct{}

	kind     string // device kind
	standing bool   // no per-session consent (one-time, support, access)
}

// Hub tracks connected agents and live sessions.
type Hub struct {
	s        *Server
	mu       sync.Mutex
	agents   map[string]*agentLink
	sessions map[string]*liveSession
}

func newHub(s *Server) *Hub {
	return &Hub{s: s, agents: map[string]*agentLink{}, sessions: map[string]*liveSession{}}
}

// Online reports whether a device's control channel is connected.
func (h *Hub) Online(deviceID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[deviceID] != nil
}

func (h *Hub) agent(deviceID string) *agentLink {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[deviceID]
}

func (h *Hub) session(id string) *liveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

func sixDigits() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// CreateSession starts an attended support request.
func (h *Hub) CreateSession(u *User, d *Device, perms protocol.Perms) (*liveSession, string, error) {
	link := h.agent(d.ID)
	if link == nil {
		return nil, "", errors.New("device is offline")
	}
	ls := &liveSession{
		id: "ses-" + RandomToken(10), deviceID: d.ID, deviceName: d.Hostname, clientID: d.ClientID,
		userID: u.ID, techName: u.Name, techEmail: u.Email,
		requested: perms, code: sixDigits(), expires: time.Now().Add(h.s.cfg.ConsentTimeout),
		state: statePending, denied: map[string]bool{}, clipCounts: map[string]int{},
		kind: d.Kind, standing: d.Kind != "" && d.Kind != protocol.KindAttended,
	}
	if ls.standing {
		// The endpoint consented when the client was run or installed. The
		// session is capped by what it allowed then, and the bar is shown.
		ls.granted = perms.Intersect(protocol.ParsePerms(d.Standing))
		if !ls.granted.Has(protocol.PermView) {
			return nil, "", errors.New("the endpoint did not allow screen sharing")
		}
		ls.state, ls.code = stateActive, ""
	}
	ticket := "rsm-ticket-" + RandomToken(24)
	ls.ticket = HashToken(ticket)
	ls.ticketExp = time.Now().Add(h.s.cfg.ConsentTimeout)
	if err := h.s.store.CreateSupportSession(ls.id, d.ID, u.ID, perms.List()); err != nil {
		return nil, "", err
	}
	h.mu.Lock()
	h.sessions[ls.id] = ls
	h.mu.Unlock()
	if ls.standing {
		_ = h.s.store.MarkSessionActive(ls.id, ls.granted.List())
		h.audit(ls, u.Email, "session.standing_consent", map[string]any{"kind": d.Kind, "granted": ls.granted.List()})
		link.c.sendMsg(protocol.SessionStart{T: protocol.TSessionStart, SessionID: ls.id, Standing: true, Technician: u.Name, TechEmail: u.Email}, nil)
	} else {
		link.c.sendMsg(protocol.SessionRequest{T: protocol.TSessionRequest, SessionID: ls.id, Technician: u.Name, TechEmail: u.Email,
			Org: h.s.cfg.OrgName, Perms: perms.List(), ExpiresAt: ls.expires.Unix()}, nil)
	}
	go h.watch(ls)
	return ls, ticket, nil
}

// watch expires pending sessions and ends sessions whose peers stay away.
func (h *Hub) watch(ls *liveSession) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		ls.mu.Lock()
		st := ls.state
		var reason string
		switch st {
		case stateEnded:
			ls.mu.Unlock()
			return
		case statePending:
			if time.Now().After(ls.expires) {
				reason = "request expired before the endpoint user accepted"
			}
		case stateActive, stateSuspended:
			g := h.s.cfg.ReconnectGrace
			if !ls.viewerLost.IsZero() && time.Since(ls.viewerLost) > g {
				reason = "technician did not reconnect"
			}
			if !ls.agentLost.IsZero() && time.Since(ls.agentLost) > g {
				reason = "endpoint did not reconnect"
			}
		}
		ls.mu.Unlock()
		if reason != "" {
			h.EndSession(ls, reason, "system")
			return
		}
	}
}

// subscribe returns a channel closed on the next state change.
func (ls *liveSession) subscribe() chan struct{} {
	ch := make(chan struct{})
	ls.stateSubs = append(ls.stateSubs, ch)
	return ch
}

func (ls *liveSession) notifyLocked() {
	for _, ch := range ls.stateSubs {
		close(ch)
	}
	ls.stateSubs = nil
}

func (ls *liveSession) stateMsg() protocol.SessionState {
	m := protocol.SessionState{T: protocol.TSessionState, State: ls.state, Reason: ls.endReason}
	if ls.state == statePending {
		m.Code = ls.code
	}
	return m
}

func (h *Hub) audit(ls *liveSession, actor, action string, details map[string]any) {
	h.s.store.Audit(AuditEntry{Actor: actor, Action: action, ClientID: ls.clientID, DeviceID: ls.deviceID, SessionID: ls.id, Details: details})
}

// Consent handles the endpoint user's answer.
func (h *Hub) Consent(deviceID string, c protocol.SessionConsent) {
	ls := h.session(c.SessionID)
	if ls == nil || ls.deviceID != deviceID {
		return
	}
	ls.mu.Lock()
	if ls.state != statePending {
		ls.mu.Unlock()
		return
	}
	if !c.Accept {
		ls.mu.Unlock()
		h.audit(ls, "endpoint-user@"+ls.deviceName, "session.declined", nil)
		h.EndSession(ls, "endpoint user declined", "endpoint")
		return
	}
	if !ConstantEq(c.Code, ls.code) {
		ls.codeTries++
		tries := ls.codeTries
		ls.mu.Unlock()
		h.audit(ls, "endpoint-user@"+ls.deviceName, "session.code_mismatch", map[string]any{"attempt": tries})
		if tries >= 3 {
			h.EndSession(ls, "support code entered incorrectly three times", "endpoint")
			return
		}
		if l := h.agent(deviceID); l != nil {
			// Ask again with the same request.
			l.c.sendMsg(protocol.SessionRequest{T: protocol.TSessionRequest, SessionID: ls.id, Technician: ls.techName, TechEmail: ls.techEmail,
				Org: h.s.cfg.OrgName, Perms: ls.requested.List(), ExpiresAt: ls.expires.Unix()}, nil)
		}
		return
	}
	ls.granted = ls.requested.Intersect(protocol.ParsePerms(c.Perms))
	if !ls.granted.Has(protocol.PermView) {
		ls.mu.Unlock()
		h.EndSession(ls, "endpoint user did not allow viewing the screen", "endpoint")
		return
	}
	ls.state = stateActive
	ls.code = ""
	granted := ls.granted.List()
	ls.notifyLocked()
	v := ls.viewer
	msg := ls.stateMsg()
	ls.mu.Unlock()
	_ = h.s.store.MarkSessionActive(ls.id, granted)
	h.audit(ls, "endpoint-user@"+ls.deviceName, "session.consented", map[string]any{"granted": granted, "technician": ls.techEmail})
	if v != nil {
		v.sendMsg(msg, nil)
	}
	if l := h.agent(deviceID); l != nil {
		l.c.sendMsg(protocol.SessionStart{T: protocol.TSessionStart, SessionID: ls.id}, nil)
	}
}

// EndSession ends a session for everyone. Ended sessions cannot resume:
// a new request and new consent are required.
func (h *Hub) EndSession(ls *liveSession, reason, by string) {
	ls.mu.Lock()
	if ls.state == stateEnded {
		ls.mu.Unlock()
		return
	}
	wasPending := ls.state == statePending
	ls.state = stateEnded
	ls.endReason = reason
	ls.notifyLocked()
	v, a := ls.viewer, ls.agent
	ls.viewer, ls.agent = nil, nil
	counts := ls.clipCounts
	ls.mu.Unlock()
	h.s.store.MarkSessionEnded(ls.id, reason)
	end := protocol.End{T: protocol.TSessionEnd, Reason: reason}
	if v != nil {
		v.sendMsg(protocol.SessionState{T: protocol.TSessionState, State: stateEnded, Reason: reason}, nil)
		v.sendMsg(end, nil)
		v.close(websocket.StatusNormalClosure, "session ended")
	}
	if a != nil {
		a.sendMsg(end, nil)
		a.close(websocket.StatusNormalClosure, "session ended")
	}
	if wasPending {
		if l := h.agent(ls.deviceID); l != nil {
			l.c.sendMsg(protocol.SessionCancel{T: protocol.TSessionCancel, SessionID: ls.id, Reason: reason}, nil)
		}
	}
	h.audit(ls, by, "session.ended", map[string]any{"reason": reason, "clipboardTransfers": counts})
	if ls.kind == protocol.KindOneTime {
		// A one-time client is only good for one session.
		go h.s.removeDeviceByID(ls.deviceID, "the one-time support session ended")
	}
	// Keep ended sessions briefly so late reconnects get a clear answer.
	time.AfterFunc(10*time.Minute, func() {
		h.mu.Lock()
		delete(h.sessions, ls.id)
		h.mu.Unlock()
	})
}

// RemoveDevice ends every session on a device and tells its client to
// uninstall itself (session deleted, access removed or revoked).
func (h *Hub) RemoveDevice(deviceID, reason string) {
	h.mu.Lock()
	list := []*liveSession{}
	for _, ls := range h.sessions {
		if ls.deviceID == deviceID {
			list = append(list, ls)
		}
	}
	link := h.agents[deviceID]
	h.mu.Unlock()
	for _, ls := range list {
		h.EndSession(ls, reason, "admin")
	}
	if link != nil {
		link.c.sendMsg(protocol.DeviceRemove{T: protocol.TDeviceRemove, Reason: reason}, nil)
		link.c.close(websocket.StatusPolicyViolation, "removed")
	}
}

// activeOn reports whether a device has a session that is not ended.
func (h *Hub) activeOn(deviceID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ls := range h.sessions {
		ls.mu.Lock()
		live := ls.deviceID == deviceID && ls.state != stateEnded
		ls.mu.Unlock()
		if live {
			return true
		}
	}
	return false
}

// attach binds a viewer or agent data connection and starts the relay.
// It returns when the connection closes.
func (h *Hub) attach(ls *liveSession, c *conn, isViewer bool) {
	ls.mu.Lock()
	if ls.state == stateEnded {
		ls.mu.Unlock()
		c.sendMsg(protocol.SessionState{T: protocol.TSessionState, State: stateEnded, Reason: ls.endReason}, nil)
		c.close(websocket.StatusPolicyViolation, "session ended")
		return
	}
	if !isViewer && ls.state == statePending {
		ls.mu.Unlock()
		c.close(websocket.StatusPolicyViolation, "no consent")
		return
	}
	var old *conn
	if isViewer {
		old, ls.viewer, ls.viewerLost = ls.viewer, c, time.Time{}
	} else {
		old, ls.agent, ls.agentLost = ls.agent, c, time.Time{}
	}
	if old != nil {
		old.close(websocket.StatusPolicyViolation, "replaced by a new connection")
	}
	if isViewer {
		c.sendMsg(ls.stateMsg(), nil)
	}
	h.helloLocked(ls)
	ls.mu.Unlock()

	for {
		m, err := c.read()
		if err != nil {
			break
		}
		h.route(ls, m, isViewer)
	}

	ls.mu.Lock()
	if isViewer && ls.viewer == c {
		ls.viewer, ls.viewerLost = nil, time.Now()
	}
	if !isViewer && ls.agent == c {
		ls.agent, ls.agentLost = nil, time.Now()
	}
	if ls.state == stateActive && (ls.viewer == nil || ls.agent == nil) {
		ls.state = stateSuspended
		ls.notifyLocked()
		other := ls.viewer
		if isViewer {
			other = ls.agent
		}
		if other != nil {
			other.sendMsg(protocol.SessionState{T: protocol.TSessionState, State: stateSuspended, Reason: "waiting for the other side to reconnect"}, nil)
		}
		h.audit(ls, "system", "session.suspended", map[string]any{"lost": map[bool]string{true: "technician", false: "endpoint"}[isViewer]})
	}
	ls.mu.Unlock()
}

// helloLocked sends session.hello (first time) or session.resumed (after a
// reconnect) once both sides are present. Every (re)connect gets a new
// epoch so stale clipboard data and input from before are discarded.
func (h *Hub) helloLocked(ls *liveSession) {
	if ls.viewer == nil || ls.agent == nil || (ls.state != stateActive && ls.state != stateSuspended) {
		return
	}
	t := protocol.TSessionHello
	if ls.epoch > 0 {
		t = protocol.TSessionResumed
	}
	ls.epoch++
	ls.state = stateActive
	ls.notifyLocked()
	hello := protocol.Hello{T: t, SessionID: ls.id, Epoch: ls.epoch, Perms: ls.granted.List(), Technician: ls.techName, TechEmail: ls.techEmail, Device: ls.deviceName}
	ls.viewer.sendMsg(protocol.SessionState{T: protocol.TSessionState, State: stateActive}, nil)
	ls.viewer.sendMsg(hello, nil)
	ls.agent.sendMsg(hello, nil)
	if t == protocol.TSessionResumed {
		go h.audit(ls, "system", "session.resumed", map[string]any{"epoch": ls.epoch, "perms": ls.granted.List()})
	} else {
		go h.audit(ls, ls.techEmail, "session.started", map[string]any{"perms": ls.granted.List()})
	}
}

var viewerMay = map[string]bool{
	protocol.TScrSelect: true, protocol.TScrAck: true, protocol.TScrRefresh: true,
	protocol.TInMouse: true, protocol.TInKey: true, protocol.TInText: true, protocol.TInCombo: true, protocol.TInReleaseAll: true,
	protocol.TClipData: true, protocol.TClipFiles: true, protocol.TClipConfig: true,
	protocol.TFsRoots: true, protocol.TFsList: true,
	protocol.TSessionEnd: true, protocol.TPing: true, protocol.TPong: true,
}

var agentMay = map[string]bool{
	protocol.TScrMonitors: true, protocol.TScrFrame: true, protocol.TInNotice: true,
	protocol.TClipData: true, protocol.TClipFiles: true,
	protocol.TFsResult:   true,
	protocol.TSessionEnd: true, protocol.TSessionPerms: true, protocol.TPing: true, protocol.TPong: true,
}

func allowedDirection(t string, fromViewer bool) bool {
	if len(t) > 3 && t[:3] == "xf." {
		return true // both sides send and receive transfers
	}
	if fromViewer {
		return viewerMay[t]
	}
	return agentMay[t]
}

// route enforces permissions on every relayed message.
func (h *Hub) route(ls *liveSession, m *protocol.Message, fromViewer bool) {
	need, known := protocol.Class(m.Type)
	ls.mu.Lock()
	state := ls.state
	granted := ls.granted
	var to, from *conn
	if fromViewer {
		to, from = ls.agent, ls.viewer
	} else {
		to, from = ls.viewer, ls.agent
	}
	ls.mu.Unlock()

	if m.Type == protocol.TPing {
		if from != nil {
			from.sendMsg(map[string]string{"t": protocol.TPong}, nil)
		}
		return
	}
	if m.Type == protocol.TSessionEnd {
		who, actor := "technician", ls.techEmail
		if !fromViewer {
			who, actor = "endpoint user", "endpoint-user@"+ls.deviceName
		}
		h.EndSession(ls, "ended by the "+who, actor)
		return
	}
	if m.Type == protocol.TSessionPerms && !fromViewer {
		var p protocol.PermsUpdate
		if m.Into(&p) != nil {
			return
		}
		ls.mu.Lock()
		// The endpoint user can only take permissions away.
		ls.granted = ls.granted.Intersect(protocol.ParsePerms(p.Perms))
		g := ls.granted.List()
		v := ls.viewer
		ls.mu.Unlock()
		h.audit(ls, "endpoint-user@"+ls.deviceName, "session.permissions_changed", map[string]any{"granted": g})
		if v != nil {
			v.sendMsg(protocol.PermsUpdate{T: protocol.TSessionPerms, Perms: g}, nil)
		}
		if !protocol.ParsePerms(g).Has(protocol.PermView) {
			h.EndSession(ls, "endpoint user stopped screen sharing", "endpoint-user@"+ls.deviceName)
		}
		return
	}
	if !known || !allowedDirection(m.Type, fromViewer) {
		h.deny(ls, from, m.Type, "invalid")
		return
	}
	if state != stateActive {
		return // dropped while suspended; peers resynchronise on resume
	}
	if need != "" && !granted.Has(need) {
		h.deny(ls, from, m.Type, string(need))
		return
	}
	h.observe(ls, m, fromViewer)
	if to != nil {
		b, _ := protocol.Encode(json.RawMessage(m.Header), m.Payload)
		to.send(b)
	}
}

func (h *Hub) deny(ls *liveSession, from *conn, t, need string) {
	ls.mu.Lock()
	first := !ls.denied[t]
	ls.denied[t] = true
	ls.mu.Unlock()
	if from != nil {
		from.sendMsg(protocol.Denied{T: protocol.TDenied, Blocked: t, Need: need}, nil)
	}
	if first {
		h.audit(ls, "system", "session.message_denied", map[string]any{"type": t, "needs": need})
	}
}

// observe records metadata-only audit events for transfers and clipboard.
func (h *Hub) observe(ls *liveSession, m *protocol.Message, fromViewer bool) {
	dir := map[bool]string{true: "technician->endpoint", false: "endpoint->technician"}[fromViewer]
	switch m.Type {
	case protocol.TXfBegin:
		var b struct {
			Items []struct {
				Rel  string `json:"rel"`
				Dir  bool   `json:"dir"`
				Size int64  `json:"size"`
			} `json:"items"`
			Total int64  `json:"total"`
			ID    string `json:"id"`
		}
		if m.Into(&b) == nil {
			files, dirs := 0, 0
			names := []string{}
			for _, it := range b.Items {
				if it.Dir {
					dirs++
				} else {
					files++
				}
				if len(names) < 20 {
					names = append(names, it.Rel)
				}
			}
			h.audit(ls, ls.techEmail, "transfer.started", map[string]any{"direction": dir, "job": b.ID, "files": files, "folders": dirs, "bytes": b.Total, "names": names})
		}
	case protocol.TXfCancel:
		h.audit(ls, ls.techEmail, "transfer.cancelled", map[string]any{"direction": dir})
	case protocol.TClipData, protocol.TClipFiles:
		ls.mu.Lock()
		ls.clipCounts[dir]++
		ls.mu.Unlock()
	case protocol.TInCombo:
		var c protocol.Combo
		if m.Into(&c) == nil {
			h.audit(ls, ls.techEmail, "input.reserved_keys", map[string]any{"combo": c.Name})
		}
	}
}
