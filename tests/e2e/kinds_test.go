package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/server"
)

// runClient starts an enrolled client and returns a channel with Run's
// result and a counter of finished sessions.
func runClient(t *testing.T, p *agentcore.SimPlatform, cfg *agentcore.Config, cfgPath string) (chan error, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	var ended atomic.Int32
	a := &agentcore.Agent{P: p, Cfg: cfg, CfgPath: cfgPath, InventoryEvery: 500 * time.Millisecond,
		Log: log.New(os.Stderr, "client ", log.Lmicroseconds), SessionEnded: func(string) { ended.Add(1) }}
	go func() { errc <- a.Run(ctx) }()
	return errc, &ended
}

func noPrompt(t *testing.T) func(string) func(protocol.SessionRequest, int) agentcore.ConsentResult {
	return func(string) func(protocol.SessionRequest, int) agentcore.ConsentResult {
		return func(protocol.SessionRequest, int) agentcore.ConsentResult {
			t.Error("a standing client asked for per-session consent")
			return agentcore.ConsentResult{}
		}
	}
}

func waitRemoved(t *testing.T, errc chan error) {
	t.Helper()
	select {
	case err := <-errc:
		if !errors.Is(err, agentcore.ErrRevoked) {
			t.Fatalf("client stopped with %v, want removal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("client was not told to remove itself")
	}
}

func invite(t *testing.T, u *webUser, site int64, kind, label string, perms []string) map[string]any {
	t.Helper()
	code, r := u.do("POST", "/api/invites", map[string]any{"siteId": site, "kind": kind, "label": label, "perms": perms})
	if code != 200 {
		t.Fatalf("create %s invite: %d %v", kind, code, r)
	}
	return r
}

func sessionGranted(t *testing.T, u *webUser, sid string) []string {
	_, r := u.do("GET", "/api/sessions/"+sid, nil)
	var out []string
	for _, g := range r["granted"].([]any) {
		out = append(out, g.(string))
	}
	return out
}

func TestClientKinds(t *testing.T) {
	downloads := t.TempDir()
	os.WriteFile(filepath.Join(downloads, "RemoteSupportAgent.exe"), []byte("MZ fake agent for tests"), 0o644)
	e := setupWith(t, func(c *server.Config) {
		c.DownloadsDir = downloads
		c.OneTimeGrace = 300 * time.Millisecond
	})
	all := []string{"view", "control", "clipboard", "transfer"}

	t.Run("one-time client joins with a code and is removed after the session", func(t *testing.T) {
		r := invite(t, e.tech, e.siteA, protocol.KindOneTime, "Front desk call", all)
		code := r["code"].(string)
		if len(code) != 6 {
			t.Fatalf("code %q", code)
		}
		info, err := agentcore.FetchInvite(e.ts.URL, e.pin, "", code)
		if err != nil || info.Kind != protocol.KindOneTime || info.Technician != "Taylor Tech" || info.Org != "Example Support Co" {
			t.Fatalf("invite info: %+v %v", info, err)
		}
		if _, err := agentcore.FetchInvite(e.ts.URL, e.pin, "", "000000"); err == nil && code != "000000" {
			t.Fatal("unknown code accepted")
		}
		p := agentcore.NewSim(t.TempDir())
		// The end user unticks control and clipboard.
		cfg, err := agentcore.Join(p, e.ts.URL, e.pin, "", code, []string{"view", "transfer"}, "")
		if err != nil || cfg.Kind != protocol.KindOneTime {
			t.Fatalf("join: %+v %v", cfg, err)
		}
		if _, err := agentcore.Join(p, e.ts.URL, e.pin, "", code, all, ""); err == nil {
			t.Fatal("one-time code used twice")
		}
		errc, _ := runClient(t, p, cfg, "")
		waitFor(t, "one-time online", func() bool {
			_, r := e.tech.do("GET", "/api/devices/"+cfg.DeviceID, nil)
			return dev(r)["online"] == true
		})

		v, sid := startSession(t, e, e.tech, cfg.DeviceID, all, noPrompt(t), p)
		v.waitActive(t)
		if g := sessionGranted(t, e.tech, sid); len(g) != 2 || g[0] != "view" || g[1] != "transfer" {
			t.Fatalf("granted %v, want what the end user allowed", g)
		}
		if p.LastIndicator() == nil {
			t.Fatal("session bar not shown")
		}
		if code, _ := e.tech.do("POST", "/api/sessions/"+sid+"/end", nil); code != 200 {
			t.Fatal("end session")
		}
		waitRemoved(t, errc)
		waitFor(t, "device revoked", func() bool {
			_, r := e.tech.do("GET", "/api/devices/"+cfg.DeviceID, nil)
			return dev(r)["revoked"] == true
		})
	})

	t.Run("one-time client closed by the end user is removed", func(t *testing.T) {
		r := invite(t, e.tech, e.siteA, protocol.KindOneTime, "Closed early", all)
		p := agentcore.NewSim(t.TempDir())
		cfg, err := agentcore.Join(p, e.ts.URL, e.pin, "", r["code"].(string), all, "")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		a := &agentcore.Agent{P: p, Cfg: cfg, InventoryEvery: time.Second, Log: log.New(io.Discard, "", 0)}
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()
		waitFor(t, "online", func() bool {
			_, r := e.tech.do("GET", "/api/devices/"+cfg.DeviceID, nil)
			return dev(r)["online"] == true
		})
		cancel()
		<-done
		waitFor(t, "removed after grace", func() bool {
			_, r := e.tech.do("GET", "/api/devices/"+cfg.DeviceID, nil)
			return dev(r)["revoked"] == true
		})
	})

	t.Run("support client stays until the technician deletes it, then uninstalls", func(t *testing.T) {
		r := invite(t, e.tech, e.siteA, protocol.KindSupport, "Reception PC", all)
		tok := r["token"].(string)
		// The download carries the invitation next to the program.
		resp, err := newClient(e.pin).Get(r["download"].(string))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("download: %v %v", err, resp)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		files := map[string][]byte{}
		for _, f := range zr.File {
			rc, _ := f.Open()
			files[f.Name], _ = io.ReadAll(rc)
			rc.Close()
		}
		var cc server.ClientConfig
		if json.Unmarshal(files["client.json"], &cc) != nil || cc.Invite != tok || cc.Kind != protocol.KindSupport || cc.Server != e.ts.URL {
			t.Fatalf("client.json: %s", files["client.json"])
		}
		if string(files["RemoteSupportAgent.exe"]) != "MZ fake agent for tests" || len(files["README.txt"]) == 0 {
			t.Fatal("zip contents")
		}
		p := agentcore.NewSim(t.TempDir())
		cfgPath := filepath.Join(t.TempDir(), "agent.json")
		cfg, err := agentcore.Join(p, cc.Server, e.pin, cc.Invite, "", all, cfgPath)
		if err != nil || cfg.Kind != protocol.KindSupport || cfg.Label != "Reception PC" {
			t.Fatalf("join: %+v %v", cfg, err)
		}
		errc, ended := runClient(t, p, cfg, cfgPath)
		waitFor(t, "support online", func() bool {
			_, r := e.tech.do("GET", "/api/devices/"+cfg.DeviceID, nil)
			return dev(r)["online"] == true
		})
		for i := 0; i < 2; i++ { // reconnect as often as needed
			v, sid := startSession(t, e, e.tech, cfg.DeviceID, []string{"view", "control"}, noPrompt(t), p)
			v.waitActive(t)
			e.tech.do("POST", "/api/sessions/"+sid+"/end", nil)
			want := int32(i + 1)
			waitFor(t, "session ended", func() bool { return ended.Load() == want })
		}
		select {
		case err := <-errc:
			t.Fatalf("support client stopped after a session: %v", err)
		default:
		}
		// The other technician cannot see or remove it.
		if code, _ := e.other.do("POST", "/api/devices/"+cfg.DeviceID+"/remove", nil); code != 404 {
			t.Fatalf("cross-client remove = %d", code)
		}
		if code, _ := e.tech.do("POST", "/api/devices/"+cfg.DeviceID+"/remove", nil); code != 200 {
			t.Fatal("remove")
		}
		waitRemoved(t, errc)
		_, a := e.admin.do("GET", "/api/audit", nil)
		if !bytes.Contains(mustJSON(a), []byte("device.removed")) || !bytes.Contains(mustJSON(a), []byte("session.standing_consent")) {
			t.Fatal("audit is missing removal or standing consent")
		}
	})

	t.Run("unattended access needs an administrator", func(t *testing.T) {
		if code, _ := e.tech.do("POST", "/api/invites", map[string]any{"siteId": e.siteA, "kind": protocol.KindAccess, "label": "Server room", "perms": all}); code != 403 {
			t.Fatalf("technician created unattended installer: %d", code)
		}
		r := invite(t, e.admin, e.siteA, protocol.KindAccess, "Lab PC 1", all)
		p := agentcore.NewSim(t.TempDir())
		cfg, err := agentcore.Join(p, e.ts.URL, e.pin, r["token"].(string), "", nil, filepath.Join(t.TempDir(), "agent.json"))
		if err != nil || cfg.Kind != protocol.KindAccess {
			t.Fatalf("join: %+v %v", cfg, err)
		}
		errc, _ := runClient(t, p, cfg, "")
		waitFor(t, "access online", func() bool {
			_, r := e.tech.do("GET", "/api/devices/"+cfg.DeviceID, nil)
			return dev(r)["online"] == true
		})
		v, sid := startSession(t, e, e.tech, cfg.DeviceID, all, noPrompt(t), p)
		v.waitActive(t)
		if p.LastIndicator() == nil {
			t.Fatal("session bar not shown for unattended session")
		}
		e.tech.do("POST", "/api/sessions/"+sid+"/end", nil)
		if code, _ := e.tech.do("POST", "/api/devices/"+cfg.DeviceID+"/remove", nil); code != 403 {
			t.Fatalf("technician removed unattended access: %d", code)
		}
		if code, _ := e.admin.do("POST", "/api/devices/"+cfg.DeviceID+"/remove", nil); code != 200 {
			t.Fatal("admin remove")
		}
		waitRemoved(t, errc)
	})
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
