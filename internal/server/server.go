// Package server is the self-hostable Remote Support Manager server: the
// management console, the REST API, the agent and viewer WebSocket
// endpoints and the permission-enforcing session relay.
package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"rsc.io/qr"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

//go:embed web
var webFS embed.FS

// Version is set at build time.
var Version = "0.1.0-dev"

// Config holds server settings.
type Config struct {
	DataDir        string
	OrgName        string
	ConsentTimeout time.Duration
	ReconnectGrace time.Duration
	SessionTTL     time.Duration
	UpdatesDir     string
	// CertPin is the SPKI SHA-256 of the server certificate, shown in the
	// console so enrollment commands can pin it.
	CertPin string
	// TrustProxyHeaders is never enabled by default; the server is meant
	// to terminate TLS itself.
	Logger *log.Logger
}

// Server is the HTTP handler.
type Server struct {
	cfg     Config
	store   *Store
	hub     *Hub
	pending pendingStore
	limiter ipLimiter
	mux     *http.ServeMux
	log     *log.Logger
}

// New creates a server over an open store.
func New(cfg Config, store *Store) *Server {
	if cfg.ConsentTimeout == 0 {
		cfg.ConsentTimeout = 5 * time.Minute
	}
	if cfg.ReconnectGrace == 0 {
		cfg.ReconnectGrace = 60 * time.Second
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.OrgName == "" {
		cfg.OrgName = "Remote Support"
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "rsm-server ", log.LstdFlags)
	}
	s := &Server{cfg: cfg, store: store, log: cfg.Logger}
	s.hub = newHub(s)
	s.routes()
	return s
}

// Store exposes the database (used by the CLI and tests).
func (s *Server) Store() *Store { return s.store }

func (s *Server) routes() {
	m := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	m.Handle("GET /", http.FileServer(http.FS(web)))

	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/mfa/verify", s.handleMFAVerify)
	m.HandleFunc("GET /api/mfa/qr", s.handleMFAQR)
	m.HandleFunc("POST /api/logout", s.authed(s.handleLogout))
	m.HandleFunc("GET /api/me", s.authed(s.handleMe))
	m.HandleFunc("POST /api/me/password", s.authed(s.handlePassword))
	m.HandleFunc("GET /api/tree", s.authed(s.handleTree))
	m.HandleFunc("GET /api/devices/{id}", s.authed(s.handleDevice))
	m.HandleFunc("POST /api/devices/{id}/sessions", s.authed(s.handleCreateSession))
	m.HandleFunc("GET /api/sessions/{id}", s.authed(s.handleSessionStatus))
	m.HandleFunc("POST /api/sessions/{id}/end", s.authed(s.handleEndSessionAPI))
	m.HandleFunc("GET /api/audit", s.authed(s.handleAudit))

	m.HandleFunc("POST /api/admin/clients", s.admin(s.handleCreateClient))
	m.HandleFunc("POST /api/admin/sites", s.admin(s.handleCreateSite))
	m.HandleFunc("POST /api/admin/enroll-tokens", s.admin(s.handleEnrollToken))
	m.HandleFunc("POST /api/admin/devices/{id}/revoke", s.admin(s.handleRevoke))
	m.HandleFunc("GET /api/admin/users", s.admin(s.handleListUsers))
	m.HandleFunc("POST /api/admin/users", s.admin(s.handleCreateUser))
	m.HandleFunc("POST /api/admin/users/{id}/assignments", s.admin(s.handleAssign))
	m.HandleFunc("POST /api/admin/users/{id}/disable", s.admin(s.handleDisableUser))
	m.HandleFunc("POST /api/admin/users/{id}/reset-mfa", s.admin(s.handleResetMFA))

	m.HandleFunc("POST /api/viewer/redeem", s.handleRedeem)
	m.HandleFunc("GET /ws/viewer", s.handleViewerWS)

	m.HandleFunc("POST /api/agent/enroll", s.handleEnroll)
	m.HandleFunc("GET /ws/agent", s.handleAgentWS)
	m.HandleFunc("GET /ws/agent/session", s.handleAgentSessionWS)

	m.HandleFunc("GET /updates/{file}", s.handleUpdates)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux = m
}

// ServeHTTP adds security headers to every response.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

var (
	dummyOnce sync.Once
	dummyVal  string
)

func dummyHash() string {
	dummyOnce.Do(func() { dummyVal, _ = HashPassword(RandomToken(16)) })
	return dummyVal
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ---------------------------------------------------------------- principals

type principal struct {
	user    *User
	csrf    string
	token   string
	clients map[int64]protocol.Perms // nil for administrators
}

func (p *principal) clientIDs() []int64 {
	if p.clients == nil {
		return nil
	}
	ids := []int64{}
	for id := range p.clients {
		ids = append(ids, id)
	}
	return ids
}

// permsFor returns the permissions the user holds for a client, or ok=false
// if the client is not visible to them at all.
func (p *principal) permsFor(clientID int64) (protocol.Perms, bool) {
	if p.clients == nil {
		return protocol.ParsePerms([]string{"view", "control", "clipboard", "transfer"}), true
	}
	perms, ok := p.clients[clientID]
	return perms, ok
}

type authedHandler func(w http.ResponseWriter, r *http.Request, p *principal)

const cookieName = "rsm_session"

func (s *Server) principalFor(r *http.Request) (*principal, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, err
	}
	uid, csrf, err := s.store.WebSession(c.Value)
	if err != nil {
		return nil, err
	}
	u, err := s.store.UserByID(uid)
	if err != nil || u.Disabled || !u.MFAEnrolled {
		return nil, errors.New("not authorised")
	}
	p := &principal{user: u, csrf: csrf, token: c.Value}
	if !u.IsAdmin {
		as, err := s.store.Assignments(u.ID)
		if err != nil {
			return nil, err
		}
		p.clients = map[int64]protocol.Perms{}
		for cid, list := range as {
			p.clients[cid] = protocol.ParsePerms(list)
		}
	}
	return p, nil
}

func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.principalFor(r)
		if err != nil {
			errJSON(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if r.Method != http.MethodGet && !ConstantEq(r.Header.Get("X-CSRF-Token"), p.csrf) {
			errJSON(w, http.StatusForbidden, "missing or invalid CSRF token")
			return
		}
		h(w, r, p)
	}
}

func (s *Server) admin(h authedHandler) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, p *principal) {
		if !p.user.IsAdmin {
			errJSON(w, http.StatusForbidden, "administrators only")
			return
		}
		h(w, r, p)
	})
}

// ---------------------------------------------------------------- login and MFA

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip, 10, time.Minute) {
		errJSON(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, http.StatusBadRequest, "bad request")
		return
	}
	u, err := s.store.UserByEmail(req.Email)
	fail := func() {
		s.store.Audit(AuditEntry{Actor: req.Email, Action: "login.failed", IP: ip})
		errJSON(w, http.StatusUnauthorized, "email or password is incorrect")
	}
	if err != nil {
		_ = CheckPassword(dummyHash(), req.Password) // equalise timing for unknown accounts
		fail()
		return
	}
	if u.Disabled {
		fail()
		return
	}
	if u.lockedUntil > now() {
		errJSON(w, http.StatusTooManyRequests, "account temporarily locked after failed attempts")
		return
	}
	if !CheckPassword(u.passHash, req.Password) {
		f := u.failed + 1
		var lock int64
		if f >= 5 {
			lock = now() + 15*60
			f = 0
		}
		_ = s.store.SetLoginFailure(u.ID, f, lock)
		fail()
		return
	}
	_ = s.store.SetLoginFailure(u.ID, 0, 0)
	pd := &pending{userID: u.ID, expires: time.Now().Add(5 * time.Minute)}
	resp := map[string]any{}
	if !u.MFAEnrolled {
		pd.enroll = NewTOTPSecret()
		resp["enroll"] = true
		resp["secret"] = pd.enroll
		resp["uri"] = OTPAuthURI(s.cfg.OrgName, u.Email, pd.enroll)
	}
	resp["mfaToken"] = s.pending.put(pd)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMFAQR(w http.ResponseWriter, r *http.Request) {
	pd := s.pending.get(r.URL.Query().Get("token"))
	if pd == nil || pd.enroll == "" {
		http.NotFound(w, r)
		return
	}
	u, err := s.store.UserByID(pd.userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	code, err := qr.Encode(OTPAuthURI(s.cfg.OrgName, u.Email, pd.enroll), qr.M)
	if err != nil {
		http.Error(w, "qr", 500)
		return
	}
	code.Scale = 6
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(code.PNG())
}

func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow("mfa:"+ip, 10, time.Minute) {
		errJSON(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	var req struct {
		Token string `json:"mfaToken"`
		Code  string `json:"code"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, http.StatusBadRequest, "bad request")
		return
	}
	pd := s.pending.get(req.Token)
	if pd == nil {
		errJSON(w, http.StatusUnauthorized, "sign-in expired, start again")
		return
	}
	u, err := s.store.UserByID(pd.userID)
	if err != nil || u.Disabled {
		errJSON(w, http.StatusUnauthorized, "sign-in expired, start again")
		return
	}
	secret := u.totpSecret
	if pd.enroll != "" {
		secret = pd.enroll
	}
	step, ok := VerifyTOTP(secret, req.Code, time.Now())
	if ok && pd.enroll != "" {
		if err := s.store.SetTOTP(u.ID, pd.enroll); err != nil {
			errJSON(w, 500, "could not save MFA")
			return
		}
	}
	if ok {
		ok, _ = s.store.UseTOTPStep(u.ID, step) // reject replayed codes
	}
	if !ok {
		pd.tries++
		if pd.tries >= 5 {
			s.pending.del(req.Token)
		}
		s.store.Audit(AuditEntry{Actor: u.Email, Action: "login.mfa_failed", IP: ip})
		errJSON(w, http.StatusUnauthorized, "code is incorrect")
		return
	}
	s.pending.del(req.Token)
	tok, csrf, err := s.store.CreateWebSession(u.ID, s.cfg.SessionTTL)
	if err != nil {
		errJSON(w, 500, "could not sign in")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(s.cfg.SessionTTL.Seconds())})
	act := "login.succeeded"
	if pd.enroll != "" {
		act = "mfa.enrolled"
	}
	s.store.Audit(AuditEntry{Actor: u.Email, Action: act, IP: ip})
	writeJSON(w, http.StatusOK, map[string]any{"csrf": csrf, "user": u})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, p *principal) {
	s.store.DeleteWebSession(p.token)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]any{"user": p.user, "csrf": p.csrf, "org": s.cfg.OrgName, "version": Version, "pin": s.cfg.CertPin})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	if !CheckPassword(p.user.passHash, req.Current) {
		errJSON(w, 403, "current password is incorrect")
		return
	}
	if err := ValidatePassword(req.New); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	h, _ := HashPassword(req.New)
	_ = s.store.SetPassword(p.user.ID, h)
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "user.password_changed", IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- inventory

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request, p *principal) {
	tree, err := s.store.Tree(p.clientIDs(), r.URL.Query().Get("q"))
	if err != nil {
		errJSON(w, 500, "inventory unavailable")
		return
	}
	for _, c := range tree {
		for _, st := range c.Sites {
			for _, d := range st.Devices {
				d.Online = s.hub.Online(d.ID)
			}
			sort.SliceStable(st.Devices, func(i, j int) bool { return st.Devices[i].Online && !st.Devices[j].Online })
		}
	}
	perms := map[int64][]string{}
	for _, c := range tree {
		pp, _ := p.permsFor(c.ID)
		perms[c.ID] = pp.List()
	}
	writeJSON(w, 200, map[string]any{"clients": tree, "perms": perms})
}

// visibleDevice loads a device and hides it (404) from users without access
// to its client, so another client's devices cannot even be probed.
func (s *Server) visibleDevice(w http.ResponseWriter, r *http.Request, p *principal) (*Device, protocol.Perms, bool) {
	d, err := s.store.Device(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "device not found")
		return nil, nil, false
	}
	perms, ok := p.permsFor(d.ClientID)
	if !ok {
		s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "access.denied", ClientID: d.ClientID, DeviceID: d.ID, IP: clientIP(r), Details: map[string]any{"path": r.URL.Path}})
		errJSON(w, 404, "device not found")
		return nil, nil, false
	}
	d.Online = s.hub.Online(d.ID)
	return d, perms, true
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request, p *principal) {
	d, perms, ok := s.visibleDevice(w, r, p)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"device": d, "perms": perms.List()})
}

// ---------------------------------------------------------------- sessions

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request, p *principal) {
	d, allowed, ok := s.visibleDevice(w, r, p)
	if !ok {
		return
	}
	if d.Revoked {
		errJSON(w, 409, "device is revoked")
		return
	}
	var req struct {
		Perms []string `json:"perms"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	want := protocol.ParsePerms(req.Perms)
	want[protocol.PermView] = true
	perms := want.Intersect(allowed)
	if !perms.Has(protocol.PermView) {
		errJSON(w, 403, "you are not allowed to view this device")
		return
	}
	ls, ticket, err := s.hub.CreateSession(p.user, d, perms)
	if err != nil {
		errJSON(w, 409, err.Error())
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "session.requested", ClientID: d.ClientID, DeviceID: d.ID, SessionID: ls.id, IP: clientIP(r), Details: map[string]any{"perms": perms.List()}})
	writeJSON(w, 200, map[string]any{"sessionId": ls.id, "code": ls.code, "ticket": ticket, "expiresAt": ls.expires.Unix(), "perms": perms.List()})
}

func (s *Server) ownSession(w http.ResponseWriter, r *http.Request, p *principal) *liveSession {
	ls := s.hub.session(r.PathValue("id"))
	if ls == nil || (ls.userID != p.user.ID && !p.user.IsAdmin) {
		errJSON(w, 404, "session not found")
		return nil
	}
	if _, ok := p.permsFor(ls.clientID); !ok {
		errJSON(w, 404, "session not found")
		return nil
	}
	return ls
}

func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	ls := s.ownSession(w, r, p)
	if ls == nil {
		return
	}
	ls.mu.Lock()
	m := ls.stateMsg()
	g := ls.granted.List()
	ls.mu.Unlock()
	writeJSON(w, 200, map[string]any{"state": m.State, "code": m.Code, "reason": m.Reason, "granted": g})
}

func (s *Server) handleEndSessionAPI(w http.ResponseWriter, r *http.Request, p *principal) {
	ls := s.ownSession(w, r, p)
	if ls == nil {
		return
	}
	s.hub.EndSession(ls, "ended by the technician", p.user.Email)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleRedeem swaps the one-time launch ticket from the console for a
// bearer token that the native viewer uses for this session only.
func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("redeem:"+clientIP(r), 20, time.Minute) {
		errJSON(w, 429, "too many attempts")
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
		Ticket    string `json:"ticket"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	ls := s.hub.session(req.SessionID)
	if ls == nil {
		errJSON(w, 404, "session not found")
		return
	}
	ls.mu.Lock()
	valid := !ls.ticketUsed && time.Now().Before(ls.ticketExp) && ConstantEq(ls.ticket, HashToken(req.Ticket)) && ls.state != stateEnded
	var token string
	if valid {
		ls.ticketUsed = true
		token = "rsm-viewer-" + RandomToken(32)
		ls.viewerToken = HashToken(token)
	}
	info := map[string]any{"sessionId": ls.id, "device": ls.deviceName, "technician": ls.techName, "requested": ls.requested.List(), "state": ls.state, "code": ls.code}
	ls.mu.Unlock()
	if !valid {
		errJSON(w, 403, "launch link is invalid, expired or already used")
		return
	}
	info["token"] = token
	s.hub.audit(ls, ls.techEmail, "viewer.launched", map[string]any{"ip": clientIP(r)})
	writeJSON(w, 200, info)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func (s *Server) handleViewerWS(w http.ResponseWriter, r *http.Request) {
	ls := s.hub.session(r.URL.Query().Get("sid"))
	tok := bearer(r)
	if ls == nil || tok == "" {
		http.Error(w, "not found", 404)
		return
	}
	ls.mu.Lock()
	ok := ls.viewerToken != "" && ConstantEq(ls.viewerToken, HashToken(tok))
	ended := ls.state == stateEnded
	reason := ls.endReason
	ls.mu.Unlock()
	if !ok {
		http.Error(w, "not authorised", 401)
		return
	}
	if ended {
		// Consent was withdrawn or the session finished: never resume.
		writeJSON(w, http.StatusGone, map[string]string{"error": "session ended", "reason": reason})
		return
	}
	// A disabled technician or one who lost access cannot reconnect.
	if u, err := s.store.UserByID(ls.userID); err != nil || u.Disabled {
		http.Error(w, "not authorised", 401)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c := newConn(context.Background(), ws)
	s.hub.attach(ls, c, true)
	c.close(websocket.StatusNormalClosure, "")
}

// ---------------------------------------------------------------- agents

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow("enroll:"+ip, 10, time.Minute) {
		errJSON(w, 429, "too many attempts")
		return
	}
	var req struct {
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	id, secret, siteID, err := s.store.Enroll(strings.TrimSpace(req.Token), clip(req.Hostname, 255))
	if err != nil {
		s.store.Audit(AuditEntry{Actor: "agent@" + ip, Action: "device.enroll_failed", IP: ip})
		errJSON(w, 403, err.Error())
		return
	}
	cid, _ := s.store.SiteClient(siteID)
	s.store.Audit(AuditEntry{Actor: "agent@" + ip, Action: "device.enrolled", ClientID: cid, DeviceID: id, IP: ip, Details: map[string]any{"hostname": req.Hostname, "siteId": siteID}})
	writeJSON(w, 200, map[string]string{"deviceId": id, "secret": secret})
}

func (s *Server) authDevice(r *http.Request) (*Device, error) {
	tok := bearer(r)
	id, secret, ok := strings.Cut(tok, ".")
	if !ok {
		return nil, errors.New("no credential")
	}
	return s.store.AuthDevice(id, secret)
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	d, err := s.authDevice(r)
	if err != nil {
		http.Error(w, "not authorised", 401)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c := newConn(context.Background(), ws)
	link := &agentLink{deviceID: d.ID, clientID: d.ClientID, c: c}
	s.hub.mu.Lock()
	if old := s.hub.agents[d.ID]; old != nil {
		old.c.close(websocket.StatusPolicyViolation, "replaced")
	}
	s.hub.agents[d.ID] = link
	s.hub.mu.Unlock()
	s.store.Touch(d.ID)
	defer func() {
		s.hub.mu.Lock()
		if s.hub.agents[d.ID] == link {
			delete(s.hub.agents, d.ID)
		}
		s.hub.mu.Unlock()
		s.store.Touch(d.ID)
		c.close(websocket.StatusNormalClosure, "")
	}()
	for {
		m, err := c.read()
		if err != nil {
			return
		}
		switch m.Type {
		case protocol.TAgentInventory:
			var inv Inventory
			if m.Into(&inv) == nil {
				_ = s.store.UpdateInventory(d.ID, inv)
			}
		case protocol.TSessionConsent:
			var sc protocol.SessionConsent
			if m.Into(&sc) == nil {
				s.hub.Consent(d.ID, sc)
			}
		case protocol.TPing:
			s.store.Touch(d.ID)
			c.sendMsg(map[string]string{"t": protocol.TPong}, nil)
		}
		// Re-check revocation on every message so a revoked device is cut off.
		if dd, err := s.store.Device(d.ID); err != nil || dd.Revoked {
			c.sendMsg(map[string]string{"t": protocol.TDeviceRevoked}, nil)
			return
		}
	}
}

func (s *Server) handleAgentSessionWS(w http.ResponseWriter, r *http.Request) {
	d, err := s.authDevice(r)
	if err != nil {
		http.Error(w, "not authorised", 401)
		return
	}
	ls := s.hub.session(r.URL.Query().Get("sid"))
	if ls == nil || ls.deviceID != d.ID {
		http.Error(w, "not found", 404)
		return
	}
	ls.mu.Lock()
	ended := ls.state == stateEnded
	ls.mu.Unlock()
	if ended {
		writeJSON(w, http.StatusGone, map[string]string{"error": "session ended"})
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c := newConn(context.Background(), ws)
	s.hub.attach(ls, c, false)
	c.close(websocket.StatusNormalClosure, "")
}

// ---------------------------------------------------------------- admin

func (s *Server) handleCreateClient(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name string `json:"name"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	id, err := s.store.CreateClient(req.Name)
	if err != nil {
		errJSON(w, 400, "could not create client: "+err.Error())
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "client.created", ClientID: id, IP: clientIP(r), Details: map[string]any{"name": req.Name}})
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		ClientID int64  `json:"clientId"`
		Name     string `json:"name"`
	}
	if readJSON(r, &req) != nil || !s.store.ClientExists(req.ClientID) {
		errJSON(w, 400, "bad request")
		return
	}
	id, err := s.store.CreateSite(req.ClientID, req.Name)
	if err != nil {
		errJSON(w, 400, "could not create site: "+err.Error())
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "site.created", ClientID: req.ClientID, IP: clientIP(r), Details: map[string]any{"name": req.Name, "siteId": id}})
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (s *Server) handleEnrollToken(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		SiteID int64 `json:"siteId"`
		Hours  int   `json:"hours"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	cid, err := s.store.SiteClient(req.SiteID)
	if err != nil {
		errJSON(w, 404, "site not found")
		return
	}
	if req.Hours <= 0 || req.Hours > 72 {
		req.Hours = 24
	}
	tok, exp, err := s.store.CreateEnrollToken(req.SiteID, p.user.ID, time.Duration(req.Hours)*time.Hour)
	if err != nil {
		errJSON(w, 500, "could not create token")
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "enroll_token.created", ClientID: cid, IP: clientIP(r), Details: map[string]any{"siteId": req.SiteID, "expiresAt": exp}})
	writeJSON(w, 200, map[string]any{"token": tok, "expiresAt": exp})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request, p *principal) {
	d, _, ok := s.visibleDevice(w, r, p)
	if !ok {
		return
	}
	if err := s.store.RevokeDevice(d.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errJSON(w, 409, "already revoked")
			return
		}
		errJSON(w, 500, "could not revoke")
		return
	}
	s.hub.EndDeviceSessions(d.ID, "device access was revoked")
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "device.revoked", ClientID: d.ClientID, DeviceID: d.ID, IP: clientIP(r), Details: map[string]any{"hostname": d.Hostname}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, p *principal) {
	users, err := s.store.ListUsers()
	if err != nil {
		errJSON(w, 500, "error")
		return
	}
	out := []map[string]any{}
	for _, u := range users {
		as, _ := s.store.Assignments(u.ID)
		out = append(out, map[string]any{"user": u, "assignments": as})
	}
	writeJSON(w, 200, map[string]any{"users": out})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Admin    bool   `json:"admin"`
	}
	if readJSON(r, &req) != nil || !strings.Contains(req.Email, "@") || strings.TrimSpace(req.Name) == "" {
		errJSON(w, 400, "email and name are required")
		return
	}
	if err := ValidatePassword(req.Password); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	h, _ := HashPassword(req.Password)
	id, err := s.store.CreateUser(req.Email, req.Name, h, req.Admin, true)
	if err != nil {
		errJSON(w, 409, "a user with that email already exists")
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "user.created", IP: clientIP(r), Details: map[string]any{"email": req.Email, "admin": req.Admin}})
	writeJSON(w, 200, map[string]int64{"id": id})
}

func pathID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request, p *principal) {
	uid, err := pathID(r)
	if err != nil {
		errJSON(w, 400, "bad id")
		return
	}
	var req struct {
		ClientID int64    `json:"clientId"`
		Perms    []string `json:"perms"`
	}
	if readJSON(r, &req) != nil || !s.store.ClientExists(req.ClientID) {
		errJSON(w, 400, "bad request")
		return
	}
	perms := protocol.ParsePerms(req.Perms).List()
	if err := s.store.SetAssignment(uid, req.ClientID, perms); err != nil {
		errJSON(w, 500, "could not save")
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "user.assignment_changed", ClientID: req.ClientID, IP: clientIP(r), Details: map[string]any{"userId": uid, "perms": perms}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDisableUser(w http.ResponseWriter, r *http.Request, p *principal) {
	uid, err := pathID(r)
	if err != nil || uid == p.user.ID {
		errJSON(w, 400, "you cannot disable yourself")
		return
	}
	var req struct {
		Disabled bool `json:"disabled"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "bad request")
		return
	}
	_ = s.store.SetDisabled(uid, req.Disabled)
	if req.Disabled {
		s.hub.mu.Lock()
		list := []*liveSession{}
		for _, ls := range s.hub.sessions {
			if ls.userID == uid {
				list = append(list, ls)
			}
		}
		s.hub.mu.Unlock()
		for _, ls := range list {
			s.hub.EndSession(ls, "technician account was disabled", p.user.Email)
		}
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "user.disabled_changed", IP: clientIP(r), Details: map[string]any{"userId": uid, "disabled": req.Disabled}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleResetMFA(w http.ResponseWriter, r *http.Request, p *principal) {
	uid, err := pathID(r)
	if err != nil {
		errJSON(w, 400, "bad id")
		return
	}
	_ = s.store.SetTOTP(uid, "")
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "user.mfa_reset", IP: clientIP(r), Details: map[string]any{"userId": uid}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	list, err := s.store.AuditList(p.clientIDs(), limit)
	if err != nil {
		errJSON(w, 500, "error")
		return
	}
	writeJSON(w, 200, map[string]any{"entries": list})
}

// ---------------------------------------------------------------- updates

// handleUpdates serves signed update manifests and packages from a folder.
// Clients verify the manifest signature and the package hash themselves.
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UpdatesDir == "" {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("file")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.cfg.UpdatesDir, name)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	// http.ServeFile honours If-None-Match and Range when an ETag is set.
	w.Header().Set("ETag", `"`+strconv.FormatInt(st.Size(), 16)+"-"+strconv.FormatInt(st.ModTime().UnixNano(), 16)+`"`)
	http.ServeFile(w, r, p)
}
