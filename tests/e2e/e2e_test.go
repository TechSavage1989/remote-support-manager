// Package e2e runs the real server, the agent core with the simulated
// endpoint platform, and the viewer core over TLS WebSockets in one process.
// It covers the workflows that do not need a real Windows desktop; the
// Windows-specific checks are listed in docs/TESTING.md.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/server"
	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
	"github.com/TechSavage1989/remote-support-manager/internal/transfer"
	"github.com/TechSavage1989/remote-support-manager/internal/viewercore"
)

type env struct {
	t     *testing.T
	ts    *httptest.Server
	srv   *server.Server
	pin   string
	admin *webUser
	tech  *webUser
	other *webUser
	// device per client
	agentA, agentB *agentcore.SimPlatform
	devA, devB     string
	stopB          context.CancelFunc
	runBErr        chan error
	rootA          string
}

type webUser struct {
	t      *testing.T
	c      *http.Client
	base   string
	csrf   string
	email  string
	secret string
}

func (u *webUser) do(method, path string, body any) (int, map[string]any) {
	u.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u.csrf != "" {
		req.Header.Set("X-CSRF-Token", u.csrf)
	}
	resp, err := u.c.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func newClient(pin string) *http.Client {
	c := tlspin.HTTPClient(pin)
	c.Jar, _ = cookiejar.New(nil)
	return c
}

func login(t *testing.T, e *env, email, password string) *webUser {
	t.Helper()
	u := &webUser{t: t, c: newClient(e.pin), base: e.ts.URL, email: email}
	code, r := u.do("POST", "/api/login", map[string]string{"email": email, "password": password})
	if code != 200 {
		t.Fatalf("login %s: %d %v", email, code, r)
	}
	// Without MFA the session cookie is not issued.
	if code, _ := u.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("password alone must not sign in (got %d)", code)
	}
	if r["enroll"] != true {
		t.Fatal("first login must enrol MFA")
	}
	u.secret = r["secret"].(string)
	tok := r["mfaToken"].(string)
	if code, _ := u.do("POST", "/api/mfa/verify", map[string]string{"mfaToken": tok, "code": "000000"}); code != 401 {
		t.Fatal("wrong MFA code accepted")
	}
	otp, _ := server.TOTP(u.secret, time.Now().Unix()/30)
	code, r = u.do("POST", "/api/mfa/verify", map[string]string{"mfaToken": tok, "code": otp})
	if code != 200 {
		t.Fatalf("mfa verify: %d %v", code, r)
	}
	u.csrf = r["csrf"].(string)
	return u
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := server.OpenStore(filepath.Join(dir, "rsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := server.New(server.Config{OrgName: "Example Support Co"}, st)
	ts := httptest.NewUnstartedServer(srv)
	ts.EnableHTTP2 = false
	ts.StartTLS()
	t.Cleanup(ts.Close)
	e := &env{t: t, ts: ts, srv: srv, pin: tlspin.Fingerprint(ts.Certificate())}

	mk := func(email, name string, admin bool) {
		h, _ := server.HashPassword("correct horse battery")
		if _, err := st.CreateUser(email, name, h, admin, false); err != nil {
			t.Fatal(err)
		}
	}
	mk("admin@example.com", "Alex Admin", true)
	mk("tech@example.com", "Taylor Tech", false)
	mk("other@example.com", "Oli Other", false)
	e.admin = login(t, e, "admin@example.com", "correct horse battery")
	e.tech = login(t, e, "tech@example.com", "correct horse battery")
	e.other = login(t, e, "other@example.com", "correct horse battery")

	id := func(code int, r map[string]any) int64 {
		if code != 200 {
			t.Fatalf("admin call failed: %d %v", code, r)
		}
		return int64(r["id"].(float64))
	}
	ca := id(e.admin.do("POST", "/api/admin/clients", map[string]any{"name": "Contoso Dental (fictional)"}))
	cb := id(e.admin.do("POST", "/api/admin/clients", map[string]any{"name": "Fabrikam Law (fictional)"}))
	sa := id(e.admin.do("POST", "/api/admin/sites", map[string]any{"clientId": ca, "name": "Main office"}))
	sb := id(e.admin.do("POST", "/api/admin/sites", map[string]any{"clientId": cb, "name": "Branch"}))
	// tech -> client A only; other -> client B view only
	techID := userID(t, e, "tech@example.com")
	otherID := userID(t, e, "other@example.com")
	if c, r := e.admin.do("POST", fmt.Sprintf("/api/admin/users/%d/assignments", techID), map[string]any{"clientId": ca, "perms": []string{"view", "control", "clipboard", "transfer"}}); c != 200 {
		t.Fatal(r)
	}
	if c, r := e.admin.do("POST", fmt.Sprintf("/api/admin/users/%d/assignments", otherID), map[string]any{"clientId": cb, "perms": []string{"view"}}); c != 200 {
		t.Fatal(r)
	}

	e.rootA = t.TempDir()
	e.agentA = agentcore.NewSim(e.rootA)
	e.agentB = agentcore.NewSim(t.TempDir())
	e.devA = enrollAndRun(t, e, sa, e.agentA, nil)
	ctxB, stopB := context.WithCancel(context.Background())
	e.stopB = stopB
	e.runBErr = make(chan error, 1)
	e.devB = enrollAndRunCtx(t, e, sb, e.agentB, ctxB, e.runBErr)
	t.Cleanup(stopB)
	waitFor(t, "agents online", func() bool {
		_, r := e.admin.do("GET", "/api/devices/"+e.devA, nil)
		_, r2 := e.admin.do("GET", "/api/devices/"+e.devB, nil)
		return dev(r)["online"] == true && dev(r2)["online"] == true
	})
	return e
}

func dev(r map[string]any) map[string]any {
	d, _ := r["device"].(map[string]any)
	if d == nil {
		return map[string]any{}
	}
	return d
}

func userID(t *testing.T, e *env, email string) int64 {
	_, r := e.admin.do("GET", "/api/admin/users", nil)
	for _, row := range r["users"].([]any) {
		u := row.(map[string]any)["user"].(map[string]any)
		if u["email"] == email {
			return int64(u["id"].(float64))
		}
	}
	t.Fatalf("no user %s", email)
	return 0
}

func enrollAndRun(t *testing.T, e *env, site int64, p *agentcore.SimPlatform, errc chan error) string {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return enrollAndRunCtx(t, e, site, p, ctx, errc)
}

func enrollAndRunCtx(t *testing.T, e *env, site int64, p *agentcore.SimPlatform, ctx context.Context, errc chan error) string {
	t.Helper()
	code, r := e.admin.do("POST", "/api/admin/enroll-tokens", map[string]any{"siteId": site})
	if code != 200 {
		t.Fatal(r)
	}
	tok := r["token"].(string)
	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	cfg, err := agentcore.Enroll(p, e.ts.URL, e.pin, tok, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// One-time: the same token cannot enrol a second device.
	if _, err := agentcore.Enroll(p, e.ts.URL, e.pin, tok, filepath.Join(t.TempDir(), "x.json")); err == nil {
		t.Fatal("enrollment token reused")
	}
	// A wrong pin is refused before anything is sent.
	if _, err := agentcore.Enroll(p, e.ts.URL, strings.Repeat("ab", 32), tok, filepath.Join(t.TempDir(), "y.json")); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("wrong pin should fail TLS: %v", err)
	}
	a := &agentcore.Agent{P: p, Cfg: cfg, CfgPath: cfgPath, InventoryEvery: 500 * time.Millisecond, Log: log.New(os.Stderr, "agent ", log.Lmicroseconds)}
	go func() {
		err := a.Run(ctx)
		if errc != nil {
			errc <- err
		}
	}()
	return cfg.DeviceID
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// session helpers -------------------------------------------------------

type viewer struct {
	c        *viewercore.Client
	mu       sync.Mutex
	states   []string
	frames   int
	monitors protocol.Monitors
	denied   []protocol.Denied
	notices  []protocol.Notice
	listings []viewercore.Listing
	progress map[string]transfer.Progress
	clip     clipsync.Clip
	hellos   []protocol.Hello
	ended    string
	offered  []string
	cancel   context.CancelFunc
	dropAt   int64 // drop the network once a job passes this many bytes
	dropped  bool
}

func (v *viewer) lastState() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.states) == 0 {
		return ""
	}
	return v.states[len(v.states)-1]
}

func startSession(t *testing.T, e *env, u *webUser, devID string, perms []string, consent func(code string) func(protocol.SessionRequest, int) agentcore.ConsentResult, p *agentcore.SimPlatform) (*viewer, string) {
	t.Helper()
	// The endpoint user only learns the code from the technician, so the
	// simulated user waits until the technician has it.
	codeCh := make(chan string, 1)
	var once sync.Once
	var answer func(protocol.SessionRequest, int) agentcore.ConsentResult
	p.Consent = func(req protocol.SessionRequest, attempt int) agentcore.ConsentResult {
		once.Do(func() { answer = consent(<-codeCh) })
		return answer(req, attempt)
	}
	code, r := u.do("POST", "/api/devices/"+devID+"/sessions", map[string]any{"perms": perms})
	if code != 200 {
		t.Fatalf("create session: %d %v", code, r)
	}
	sid, ticket, scode := r["sessionId"].(string), r["ticket"].(string), r["code"].(string)
	codeCh <- scode
	link := fmt.Sprintf("rsm://connect?server=%s&sid=%s&ticket=%s&pin=%s", e.ts.URL, sid, ticket, e.pin)
	l, err := viewercore.ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	c, err := viewercore.Redeem(l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := viewercore.Redeem(l); err == nil {
		t.Fatal("launch ticket reused")
	}
	v := &viewer{c: c, progress: map[string]transfer.Progress{}}
	c.CB = viewercore.Callbacks{
		State:    func(s, _, reason string) { v.mu.Lock(); v.states = append(v.states, s); v.mu.Unlock() },
		Hello:    func(h protocol.Hello, _ bool) { v.mu.Lock(); v.hellos = append(v.hellos, h); v.mu.Unlock() },
		Monitors: func(m protocol.Monitors) { v.mu.Lock(); v.monitors = m; v.mu.Unlock() },
		Denied:   func(d protocol.Denied) { v.mu.Lock(); v.denied = append(v.denied, d); v.mu.Unlock() },
		Notice:   func(n protocol.Notice) { v.mu.Lock(); v.notices = append(v.notices, n); v.mu.Unlock() },
		Listing:  func(l viewercore.Listing) { v.mu.Lock(); v.listings = append(v.listings, l); v.mu.Unlock() },
		Ended:    func(r string) { v.mu.Lock(); v.ended = r; v.mu.Unlock() },
		Transfer: func(p transfer.Progress) {
			v.mu.Lock()
			v.progress[p.JobID] = p
			drop := v.dropAt > 0 && !v.dropped && p.BytesDone > v.dropAt
			if drop {
				v.dropped = true
			}
			v.mu.Unlock()
			if drop {
				c.DropConnection()
			}
		},
		Conflict: func(ci transfer.ConflictInfo, reply func(string, bool)) { reply(transfer.Overwrite, true) },
		ApplyClipboard: func(cl clipsync.Clip) error {
			v.mu.Lock()
			v.clip = cl
			v.mu.Unlock()
			return nil
		},
		RemoteFilesCopied: func(p []string) { v.mu.Lock(); v.offered = p; v.mu.Unlock() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	t.Cleanup(cancel)
	go c.Run(ctx)
	return v, sid
}

func acceptWith(perms []string) func(code string) func(protocol.SessionRequest, int) agentcore.ConsentResult {
	return func(code string) func(protocol.SessionRequest, int) agentcore.ConsentResult {
		return func(req protocol.SessionRequest, attempt int) agentcore.ConsentResult {
			if attempt == 1 {
				return agentcore.ConsentResult{Accept: true, Code: "999999x", Perms: perms} // mistyped first
			}
			return agentcore.ConsentResult{Accept: true, Code: code, Perms: perms}
		}
	}
}

func (v *viewer) waitActive(t *testing.T) {
	waitFor(t, "session active", func() bool { return v.c.State() == "active" })
}

func randomFile(t *testing.T, p string, n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func (v *viewer) waitJob(t *testing.T, id string, states ...transfer.State) transfer.Progress {
	t.Helper()
	var last transfer.Progress
	waitFor(t, "transfer "+id, func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		last = v.progress[id]
		for _, s := range states {
			if last.State == s {
				return true
			}
		}
		return false
	})
	return last
}

// tests ---------------------------------------------------------------

func TestEndToEnd(t *testing.T) {
	e := setup(t)

	t.Run("inventory and isolation", func(t *testing.T) {
		_, r := e.tech.do("GET", "/api/tree", nil)
		b, _ := json.Marshal(r)
		s := string(b)
		if !strings.Contains(s, "Contoso Dental") || strings.Contains(s, "Fabrikam") || strings.Contains(s, e.devB) {
			t.Fatalf("technician tree must contain only client A: %s", s)
		}
		code, r := e.tech.do("GET", "/api/devices/"+e.devA, nil)
		d := dev(r)
		if code != 200 || d["osName"] != "Windows 11 Pro (simulated)" || d["agentVersion"] == "" || d["online"] != true || d["uptimeSec"].(float64) <= 0 || len(d["disks"].([]any)) != 1 || d["lastSeen"].(float64) <= 0 {
			t.Fatalf("inventory incomplete: %v", d)
		}
		// Search
		_, r = e.tech.do("GET", "/api/tree?q=simulated", nil)
		if !strings.Contains(fmt.Sprint(r), e.devA) {
			t.Fatal("search by Windows version failed")
		}
		_, r = e.tech.do("GET", "/api/tree?q=nothing-matches-this", nil)
		if len(r["clients"].([]any)) != 0 {
			t.Fatal("search should be empty")
		}
		// Cross-client access through the API is indistinguishable from "not found".
		for _, path := range []string{"/api/devices/" + e.devB} {
			if code, _ := e.tech.do("GET", path, nil); code != 404 {
				t.Fatalf("cross-client GET %s = %d", path, code)
			}
		}
		if code, _ := e.tech.do("POST", "/api/devices/"+e.devB+"/sessions", map[string]any{"perms": []string{"view"}}); code != 404 {
			t.Fatal("cross-client session allowed")
		}
		if code, _ := e.tech.do("POST", "/api/admin/devices/"+e.devB+"/revoke", nil); code != 403 {
			t.Fatal("technician reached admin API")
		}
		// CSRF required for state changes.
		saved := e.tech.csrf
		e.tech.csrf = ""
		if code, _ := e.tech.do("POST", "/api/devices/"+e.devA+"/sessions", map[string]any{"perms": []string{"view"}}); code != 403 {
			t.Fatal("missing CSRF accepted")
		}
		e.tech.csrf = saved
		// The other technician only has view on B and cannot see A.
		if code, _ := e.other.do("GET", "/api/devices/"+e.devA, nil); code != 404 {
			t.Fatal("other technician saw client A")
		}
		// Audit shows the denial for administrators but not to technicians of other clients.
		_, ar := e.admin.do("GET", "/api/audit", nil)
		if !strings.Contains(fmt.Sprint(ar), "access.denied") {
			t.Fatal("denied access not audited")
		}
		_, tr := e.other.do("GET", "/api/audit", nil)
		if strings.Contains(fmt.Sprint(tr), e.devA) {
			t.Fatal("audit leaked another client's device")
		}
	})

	t.Run("consent, permissions, screen and input", func(t *testing.T) {
		v, sid := startSession(t, e, e.tech, e.devA, []string{"view", "control", "clipboard", "transfer"}, acceptWith([]string{"view", "control", "clipboard", "transfer"}), e.agentA)
		v.waitActive(t)
		waitFor(t, "monitors", func() bool { return len(v.c.Monitors().Monitors) == 2 })
		waitFor(t, "first frame", func() bool {
			ok := false
			v.c.Canvas(func(img *image.RGBA) { ok = img != nil && img.Bounds().Dx() == 640 })
			return ok
		})
		ind := e.agentA.LastIndicator()
		if ind == nil || !strings.Contains(ind.Tech, "Taylor Tech") {
			t.Fatal("endpoint indicator must show the technician")
		}
		// Monitor switching
		_ = v.c.SelectMonitor(1)
		waitFor(t, "second monitor", func() bool {
			ok := false
			v.c.Canvas(func(img *image.RGBA) { ok = img != nil && img.Bounds().Dx() == 320 })
			return ok
		})
		// Input
		v.c.SendMouse(protocol.Mouse{X: 10, Y: 20, Action: "move"})
		v.c.SendKey(protocol.Key{Scan: 0x1D, VK: 0xA2, Down: true}) // ctrl down
		v.c.SendKey(protocol.Key{Scan: 0x2E, VK: 0x43, Down: true}) // c down, held
		_ = v.c.PasteAsKeystrokes("héllo\nworld")
		_ = v.c.Combo("alt-tab")
		_ = v.c.Combo("ctrl-alt-del")
		v.c.ReleaseKeys() // focus loss
		waitFor(t, "input delivered", func() bool {
			keys, mouse, texts := e.agentA.Snapshot()
			ups := 0
			for _, k := range keys {
				if !k.Down && (k.Scan == 0x1D || k.Scan == 0x2E) {
					ups++
				}
			}
			return len(mouse) == 1 && len(texts) == 1 && texts[0] == "héllo\nworld" && ups >= 2
		})
		waitFor(t, "ctrl-alt-del explained", func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			for _, n := range v.notices {
				if n.About == "ctrl-alt-del" && strings.Contains(n.Text, "service") {
					return true
				}
			}
			return false
		})
		// The endpoint user withdraws control from the indicator.
		ind.Hooks.ChangePerms([]string{"view", "clipboard", "transfer"})
		waitFor(t, "control withdrawn", func() bool { return !v.c.Has(protocol.PermControl) })
		before, _, _ := e.agentA.Snapshot()
		// Even a modified viewer that ignores its own check is blocked by the server.
		_ = v.c.Send(protocol.Key{T: protocol.TInKey, Scan: 0x1E, Down: true}, nil)
		waitFor(t, "server denial", func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			for _, d := range v.denied {
				if d.Blocked == protocol.TInKey && d.Need == "control" {
					return true
				}
			}
			return false
		})
		after, _, _ := e.agentA.Snapshot()
		if len(after) != len(before) {
			t.Fatal("input reached the endpoint after control was withdrawn")
		}
		_, ar := e.admin.do("GET", "/api/audit", nil)
		as := fmt.Sprint(ar)
		for _, want := range []string{"session.code_mismatch", "session.consented", "session.permissions_changed", "session.message_denied", "input.reserved_keys"} {
			if !strings.Contains(as, want) {
				t.Fatalf("audit missing %s", want)
			}
		}
		v.c.End()
		waitFor(t, "indicator closed", ind.IsClosed)
		_ = sid
	})

	t.Run("file transfer both ways, nested folders, large file, resume", func(t *testing.T) {
		v, _ := startSession(t, e, e.tech, e.devA, []string{"view", "transfer"}, acceptWith([]string{"view", "transfer"}), e.agentA)
		v.waitActive(t)
		local := t.TempDir()
		files := map[string][]byte{}
		for _, rel := range []string{"Logs/app.log", "Logs/2026/11/deep.txt", "Logs/emoji ✓ name.txt"} {
			files[rel] = randomFile(t, filepath.Join(local, filepath.FromSlash(rel)), 3000)
		}
		_ = os.MkdirAll(filepath.Join(local, "Logs", "empty"), 0o755)
		files["big.bin"] = randomFile(t, filepath.Join(local, "big.bin"), 24<<20)
		remoteDest := filepath.Join(e.rootA, "Incoming")
		_ = os.MkdirAll(remoteDest, 0o755)

		// List the remote side.
		if err := v.c.ListRemote(e.rootA); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "listing", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return len(v.listings) > 0 })

		// Interrupt the network while the large file is in flight.
		v.mu.Lock()
		v.dropAt = 6 << 20
		v.mu.Unlock()
		id, err := v.c.Upload([]string{filepath.Join(local, "Logs"), filepath.Join(local, "big.bin")}, remoteDest)
		if err != nil {
			t.Fatal(err)
		}
		p := v.waitJob(t, id, transfer.StateInterrupted, transfer.StateFailed, transfer.StateDone)
		if p.State == transfer.StateDone {
			t.Fatal("transfer finished despite the interruption")
		}
		if _, err := os.Stat(filepath.Join(remoteDest, "big.bin")); err == nil {
			t.Fatal("interrupted file must not be finalised")
		}
		partial, err := os.Stat(filepath.Join(remoteDest, "big.bin"+transfer.PartSuffix))
		if err != nil || partial.Size() == 0 {
			t.Fatalf("partial data should be kept for resume: %v", err)
		}
		t.Logf("interrupted with %d bytes of %d kept", partial.Size(), 24<<20)
		v.waitActive(t) // reconnects automatically
		id2, err := v.c.Resume(id)
		if err != nil {
			t.Fatal(err)
		}
		p = v.waitJob(t, id2, transfer.StateDone, transfer.StateFailed)
		if p.State != transfer.StateDone {
			t.Fatalf("resume failed: %+v", p)
		}
		for rel, want := range files {
			got, err := os.ReadFile(filepath.Join(remoteDest, filepath.FromSlash(rel)))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("remote %s wrong: %v", rel, err)
			}
		}
		if st, err := os.Stat(filepath.Join(remoteDest, "Logs", "empty")); err != nil || !st.IsDir() {
			t.Fatal("empty folder not preserved")
		}
		_, ar := e.admin.do("GET", "/api/audit", nil)
		if !strings.Contains(fmt.Sprint(ar), "transfer.started") || !strings.Contains(fmt.Sprint(ar), "session.resumed") {
			t.Fatal("transfer/resume not audited")
		}

		// Download the folder back to a new local folder.
		back := t.TempDir()
		id3, err := v.c.Download([]string{filepath.Join(remoteDest, "Logs")}, back)
		if err != nil {
			t.Fatal(err)
		}
		if p := v.waitJob(t, id3, transfer.StateDone, transfer.StateFailed); p.State != transfer.StateDone {
			t.Fatalf("download: %+v", p)
		}
		for rel, want := range files {
			if !strings.HasPrefix(rel, "Logs/") {
				continue
			}
			got, err := os.ReadFile(filepath.Join(back, filepath.FromSlash(rel)))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("downloaded %s wrong", rel)
			}
		}
		if _, err := os.Stat(filepath.Join(back, "Logs", "empty")); err != nil {
			t.Fatal("empty folder not preserved on download")
		}
		v.c.End()
	})

	t.Run("clipboard formats, direction, no loops, stale after reconnect", func(t *testing.T) {
		v, _ := startSession(t, e, e.tech, e.devA, []string{"view", "clipboard"}, acceptWith([]string{"view", "clipboard"}), e.agentA)
		v.waitActive(t)
		if err := v.c.SetClipboardSync(true, clipsync.Both); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		png := bytes.Repeat([]byte{1, 2, 3, 4}, 50000)
		v.c.LocalClipboardChanged(clipsync.Clip{Text: "Zeile 1\r\nZeile 2 ✓ 日本語", RTF: []byte(`{\rtf1 {\b x}}`), HTML: []byte("<b>x</b>"), PNG: png})
		waitFor(t, "clip at endpoint", func() bool {
			c, _ := e.agentA.ReadClipboard()
			return c.Text == "Zeile 1\r\nZeile 2 ✓ 日本語" && bytes.Equal(c.PNG, png) && string(c.RTF) == `{\rtf1 {\b x}}`
		})
		e.agentA.UserCopies(clipsync.Clip{Text: "from endpoint"})
		waitFor(t, "clip at technician", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.clip.Text == "from endpoint" })
		// Endpoint copies files: offered to the queue, never sent as data.
		e.agentA.UserCopies(clipsync.Clip{Files: []string{filepath.Join(e.rootA, "Incoming")}})
		time.Sleep(200 * time.Millisecond)
		v.mu.Lock()
		offered := len(v.offered)
		v.mu.Unlock()
		if offered != 0 {
			t.Fatal("files offered without transfer permission")
		}
		// Direction: technician -> endpoint only
		_ = v.c.SetClipboardSync(true, clipsync.ToRemote)
		time.Sleep(100 * time.Millisecond)
		e.agentA.UserCopies(clipsync.Clip{Text: "must not arrive"})
		time.Sleep(200 * time.Millisecond)
		v.mu.Lock()
		got := v.clip.Text
		v.mu.Unlock()
		if got == "must not arrive" {
			t.Fatal("direction control ignored")
		}
		// Reconnect: new epoch.
		_ = v.c.SetClipboardSync(true, clipsync.Both)
		v.c.DropConnection()
		waitFor(t, "resumed", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return len(v.hellos) >= 2 })
		v.mu.Lock()
		ep := v.hellos[len(v.hellos)-1].Epoch
		v.mu.Unlock()
		if ep < 2 {
			t.Fatal("epoch did not advance on reconnect")
		}
		v.c.End()
		// Clipboard stops at session end.
		time.Sleep(100 * time.Millisecond)
		e.agentA.UserCopies(clipsync.Clip{Text: "after end"})
		time.Sleep(200 * time.Millisecond)
		v.mu.Lock()
		got = v.clip.Text
		v.mu.Unlock()
		if got == "after end" {
			t.Fatal("clipboard synced after session end")
		}
		_, ar := e.admin.do("GET", "/api/audit", nil)
		as := fmt.Sprint(ar)
		for _, secret := range []string{"Zeile", "from endpoint", "must not arrive", "日本語"} {
			if strings.Contains(as, secret) {
				t.Fatalf("clipboard contents leaked into audit: %q", secret)
			}
		}
	})

	t.Run("transfer denied without permission even from a modified viewer", func(t *testing.T) {
		v, _ := startSession(t, e, e.tech, e.devA, []string{"view", "control", "transfer"}, acceptWith([]string{"view"}), e.agentA)
		v.waitActive(t)
		if v.c.Has(protocol.PermTransfer) || v.c.Has(protocol.PermControl) {
			t.Fatal("endpoint user unticked permissions, viewer still has them")
		}
		if _, err := v.c.Upload([]string{t.TempDir()}, e.rootA); err == nil {
			t.Fatal("viewer allowed upload without permission")
		}
		_ = v.c.Send(map[string]any{"t": protocol.TXfBegin, "id": "evil", "dest": e.rootA, "items": []any{map[string]any{"rel": "x.txt", "size": 1}}}, nil)
		_ = v.c.Send(map[string]any{"t": protocol.TFsList, "req": "1", "path": e.rootA}, nil)
		waitFor(t, "transfer denied", func() bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			n := 0
			for _, d := range v.denied {
				if d.Need == "transfer" {
					n++
				}
			}
			return n >= 2
		})
		v.mu.Lock()
		nl := len(v.listings)
		v.mu.Unlock()
		if nl != 0 {
			t.Fatal("listing returned without permission")
		}
		v.c.End()
	})

	t.Run("endpoint ends session; reconnect cannot revive it", func(t *testing.T) {
		v, sid := startSession(t, e, e.tech, e.devA, []string{"view"}, acceptWith([]string{"view"}), e.agentA)
		v.waitActive(t)
		waitFor(t, "indicator", func() bool { i := e.agentA.LastIndicator(); return i != nil && !i.IsClosed() })
		ind := e.agentA.LastIndicator()
		ind.Hooks.End()
		waitFor(t, "viewer ended", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.ended != "" })
		// A reconnect with the same token is refused (410 Gone).
		v.c.ReconnectWindow = time.Second
		code, r := e.tech.do("GET", "/api/sessions/"+sid, nil)
		if code != 200 || r["state"] != "ended" {
			t.Fatalf("session state %v", r)
		}
		req, _ := http.NewRequest("GET", e.ts.URL+"/ws/viewer?sid="+sid, nil)
		req.Header.Set("Authorization", "Bearer "+v.c.Token)
		resp, err := tlspin.HTTPClient(e.pin).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("reconnect after revoked consent got %d", resp.StatusCode)
		}
	})

	t.Run("declined consent", func(t *testing.T) {
		e.agentA.Consent = func(protocol.SessionRequest, int) agentcore.ConsentResult {
			return agentcore.ConsentResult{Accept: false}
		}
		code, r := e.tech.do("POST", "/api/devices/"+e.devA+"/sessions", map[string]any{"perms": []string{"view"}})
		if code != 200 {
			t.Fatal(r)
		}
		sid := r["sessionId"].(string)
		waitFor(t, "declined", func() bool {
			_, s := e.tech.do("GET", "/api/sessions/"+sid, nil)
			return s["state"] == "ended" && strings.Contains(fmt.Sprint(s["reason"]), "declined")
		})
		l := viewercore.Launch{Server: e.ts.URL, SessionID: sid, Ticket: r["ticket"].(string), Pin: e.pin}
		if _, err := viewercore.Redeem(l); err == nil {
			t.Fatal("viewer opened a declined session")
		}
	})

	t.Run("revocation", func(t *testing.T) {
		v, _ := startSession(t, e, e.other, e.devB, []string{"view", "control"}, acceptWith([]string{"view", "control"}), e.agentB)
		v.waitActive(t)
		if v.c.Has(protocol.PermControl) {
			t.Fatal("other technician has view only; control must not be granted")
		}
		if code, _ := e.admin.do("POST", "/api/admin/devices/"+e.devB+"/revoke", nil); code != 200 {
			t.Fatal("revoke failed")
		}
		waitFor(t, "session ended by revoke", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.ended != "" })
		select {
		case err := <-e.runBErr:
			if err != agentcore.ErrRevoked {
				t.Fatalf("agent stopped with %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("agent did not stop after revocation")
		}
		_, r := e.admin.do("GET", "/api/tree", nil)
		if strings.Contains(fmt.Sprint(r), e.devB) {
			t.Fatal("revoked device still listed")
		}
		if code, _ := e.other.do("POST", "/api/devices/"+e.devB+"/sessions", map[string]any{"perms": []string{"view"}}); code == 200 {
			t.Fatal("session on revoked device")
		}
	})
}
