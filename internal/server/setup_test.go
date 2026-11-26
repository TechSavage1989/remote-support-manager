package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeHost(t *testing.T) {
	good := map[string]string{
		"":                                "",
		"support.example.test":            "support.example.test",
		" HTTPS://Support.Example.test/ ": "support.example.test",
		"pc.tail1234.ts.net":              "pc.tail1234.ts.net",
	}
	for in, want := range good {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"192.0.2.10", "example", "a.example.test:443", "a.example.test/x", "user@a.example.test", "-a.example.test", "a b.test"} {
		if _, err := NormalizeHost(bad); err == nil {
			t.Errorf("NormalizeHost(%q) accepted", bad)
		}
	}
}

func newSetup(t *testing.T) (*SetupPage, *int) {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenStore(filepath.Join(dir, "rsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := 0
	p := &SetupPage{DataDir: dir, Store: st, Changed: func() { n++ },
		TailscaleName: func() (string, error) { return "pc.tail1234.ts.net", nil }}
	return p, &n
}

func post(p http.Handler, form url.Values, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/", strings.NewReader(form.Encode()))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func adminForm() url.Values {
	return url.Values{"org": {"Contoso Test"}, "mode": {"local"}, "name": {"Alex Admin"},
		"email": {"alex@example.test"}, "password": {"correct horse battery"}, "password2": {"correct horse battery"}}
}

func TestSetupPageOnlyLocal(t *testing.T) {
	p, n := newSetup(t)
	cases := map[string]func(*http.Request){
		"remote client":  func(r *http.Request) { r.RemoteAddr = "192.0.2.7:1234" },
		"rebinding host": func(r *http.Request) { r.Host = "evil.example.test:8080" },
		"cross origin":   func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.test") },
		"cross site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	}
	for name, mod := range cases {
		if w := post(p, adminForm(), mod); w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, w.Code)
		}
	}
	if us, _ := p.Store.ListUsers(); len(us) != 0 || *n != 0 {
		t.Fatal("a refused request changed something")
	}
}

func TestSetupPageFirstRunAndChanges(t *testing.T) {
	p, n := newSetup(t)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Create account and start") {
		t.Fatalf("first-run page: %d", w.Code)
	}
	f := adminForm()
	f.Set("password2", "something else entirely")
	if w := post(p, f, nil); w.Code != 400 {
		t.Fatalf("mismatched passwords: %d", w.Code)
	}
	if w := post(p, adminForm(), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	u, err := p.Store.UserByEmail("alex@example.test")
	if err != nil || !u.IsAdmin || u.MustChangePassword {
		t.Fatalf("admin not created correctly: %+v %v", u, err)
	}
	if *n != 1 {
		t.Fatal("Changed not called")
	}
	// Later changes need an administrator's password.
	change := url.Values{"org": {"Contoso Test"}, "mode": {"tailscale"}, "email": {"alex@example.test"}, "password": {"wrong password!!"}}
	if w := post(p, change, nil); w.Code != 400 {
		t.Fatalf("wrong password accepted: %d", w.Code)
	}
	change.Set("password", "correct horse battery")
	if w := post(p, change, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	s, _ := LoadSettings(p.DataDir)
	if s.PublicHost != "pc.tail1234.ts.net" || s.Tunnel != "tailscale" {
		t.Fatalf("settings: %+v", s)
	}
	change = url.Values{"org": {"Contoso Test"}, "mode": {"router"}, "host": {"192.0.2.10"}, "email": {"alex@example.test"}, "password": {"correct horse battery"}}
	if w := post(p, change, nil); w.Code != 400 {
		t.Fatalf("IP address accepted as internet name: %d", w.Code)
	}
}

func TestTunnelClientAddress(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.9")
	if got := clientIP(tunnelClient(r)); got != "203.0.113.9" {
		t.Fatalf("got %s", got)
	}
	r.RemoteAddr = "192.0.2.5:9000"
	if got := clientIP(tunnelClient(r)); got != "192.0.2.5" {
		t.Fatalf("non-loopback peer trusted header: %s", got)
	}
}
