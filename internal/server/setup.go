package server

import (
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SetupPage is the local first-run and settings page. It is served over
// plain HTTP on the loopback address only (http://127.0.0.1:8080 by default),
// so a person can set the server up in a browser on the server itself with
// no command line. It refuses requests that did not come from this computer,
// requests with a foreign Host header (DNS rebinding) and cross-site form
// posts. After the first administrator exists, changes need that
// administrator's email and password.
type SetupPage struct {
	DataDir string
	Store   *Store
	// Changed is called after settings were saved, so the HTTPS server can
	// restart with them.
	Changed func()
	// Status describes the running HTTPS server for the page.
	Status func() SetupStatus
	// TailscaleName returns this computer's Tailscale address
	// (name.tailnet.ts.net) when Tailscale is installed and signed in.
	TailscaleName func() (string, error)

	mu       sync.Mutex
	failures int
	lastFail time.Time
}

// SetupStatus is what the page shows about the running server.
type SetupStatus struct {
	ConsoleURL string // where technicians sign in
	Listening  string // for example ":443"
	CertNote   string // plain-language certificate state
	Problem    string // last error starting the server, if any
	ActionURL  string // a page the person must open to finish (e.g. allow Tailscale Funnel)
}

type setupView struct {
	NeedsAdmin bool
	S          Settings
	St         SetupStatus
	Error      string
	Saved      bool
	Version    string
	Name       string
	Email      string
	Mode       string // local, router, tailscale, cloudflare
	TSName     string
	TSProblem  string
}

func modeOf(s Settings) string {
	switch {
	case s.PublicHost == "":
		return "local"
	case s.Tunnel != "":
		return s.Tunnel
	}
	return "router"
}

func loopbackHost(h string) bool {
	host, _, err := net.SplitHostPort(h)
	if err != nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *SetupPage) allowed(r *http.Request) bool {
	if !loopbackHost(r.RemoteAddr) || !loopbackHost(r.Host) {
		return false
	}
	if r.Method == http.MethodPost {
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			return false
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host != r.Host {
				return false
			}
		}
	}
	return true
}

func (p *SetupPage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	if !p.allowed(r) {
		http.Error(w, "This page only opens on the server itself.", http.StatusForbidden)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		p.render(w, setupView{Saved: r.URL.Query().Get("saved") == "1"})
	case http.MethodPost:
		p.post(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (p *SetupPage) needsAdmin() bool {
	us, err := p.Store.ListUsers()
	return err == nil && len(us) == 0
}

func (p *SetupPage) render(w http.ResponseWriter, v setupView) {
	v.NeedsAdmin = p.needsAdmin()
	if v.S == (Settings{}) {
		v.S, _ = LoadSettings(p.DataDir)
	}
	if p.Status != nil {
		v.St = p.Status()
	}
	if v.Mode == "" {
		v.Mode = modeOf(v.S)
	}
	if p.TailscaleName != nil {
		if n, err := p.TailscaleName(); err == nil {
			v.TSName = n
		} else {
			v.TSProblem = err.Error()
		}
	}
	v.Version = Version
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if v.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = setupTmpl.Execute(w, v)
}

func (p *SetupPage) post(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	in := Settings{OrgName: strings.TrimSpace(f.Get("org")), CertEmail: strings.TrimSpace(f.Get("certEmail"))}
	mode := f.Get("mode")
	v := setupView{S: in, Name: f.Get("name"), Email: f.Get("email"), Mode: mode}
	v.S.PublicHost = f.Get("host")
	fail := func(msg string) { v.Error = msg; p.render(w, v) }
	var err error
	switch mode {
	case "local":
	case "router", "cloudflare":
		in.PublicHost, err = NormalizeHost(f.Get("host"))
		if err == nil && in.PublicHost == "" {
			err = errors.New("enter the internet address customers will use")
		}
		if mode == "cloudflare" {
			in.Tunnel = "cloudflare"
		}
	case "tailscale":
		in.Tunnel = "tailscale"
		if p.TailscaleName == nil {
			err = errors.New("Tailscale isn't available on this computer")
			break
		}
		var n string
		if n, err = p.TailscaleName(); err == nil {
			in.PublicHost, err = NormalizeHost(n)
		}
	default:
		err = errors.New("choose how customers reach this server")
	}
	if mode != "router" {
		in.CertEmail = ""
	}
	switch {
	case err != nil:
		fail(err.Error())
		return
	case in.OrgName == "" || len(in.OrgName) > 80:
		fail("Enter your organisation name (up to 80 characters). Customers see it before they agree.")
		return
	case in.CertEmail != "" && !ValidEmail(in.CertEmail):
		fail("The certificate email doesn't look right. You can also leave it empty.")
		return
	}
	email := strings.TrimSpace(f.Get("email"))
	pw := f.Get("password")
	if p.needsAdmin() {
		name := strings.TrimSpace(f.Get("name"))
		if name == "" || !ValidEmail(email) {
			fail("Enter your name and email for the administrator account.")
			return
		}
		if err := ValidatePassword(pw); err != nil {
			fail("Password: " + err.Error() + ".")
			return
		}
		if pw != f.Get("password2") {
			fail("The two passwords are different.")
			return
		}
		hash, err := HashPassword(pw)
		if err != nil {
			fail(err.Error())
			return
		}
		if _, err := p.Store.CreateUser(email, name, hash, true, false); err != nil {
			fail("Could not create the account: " + err.Error())
			return
		}
		p.Store.Audit(AuditEntry{Actor: email, Action: "setup.admin_created", IP: "local"})
	} else if !p.checkAdmin(email, pw) {
		fail("To change settings, enter an administrator's email and password.")
		return
	}
	if err := SaveSettings(p.DataDir, in); err != nil {
		fail("Could not save: " + err.Error())
		return
	}
	p.Store.Audit(AuditEntry{Actor: email, Action: "setup.settings_saved", IP: "local", Details: map[string]any{"publicHost": in.PublicHost, "org": in.OrgName}})
	if p.Changed != nil {
		p.Changed()
	}
	http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
}

func (p *SetupPage) checkAdmin(email, pw string) bool {
	p.mu.Lock()
	if p.failures >= 5 && time.Since(p.lastFail) < time.Minute {
		p.mu.Unlock()
		return false
	}
	p.mu.Unlock()
	u, err := p.Store.UserByEmail(strings.TrimSpace(email))
	ok := err == nil && u.IsAdmin && !u.Disabled && CheckPassword(u.passHash, pw)
	if !ok {
		_ = CheckPassword(dummyHash(), pw)
		p.mu.Lock()
		p.failures++
		p.lastFail = time.Now()
		p.mu.Unlock()
		return false
	}
	p.mu.Lock()
	p.failures = 0
	p.mu.Unlock()
	return true
}

var setupTmpl = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Remote Support Manager server setup</title>
<style>
:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--fg:#1d2330;--mut:#5b6475;--line:#d9dde5;--acc:#1f5fbf;--bad:#b3261e;--ok:#1d7a3a}
@media (prefers-color-scheme:dark){:root{--bg:#14171c;--card:#1d2128;--fg:#e8eaee;--mut:#a3abb9;--line:#343a45;--acc:#6aa4ff;--bad:#ff8a80;--ok:#7bd88f}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,Segoe UI,sans-serif}
main{max-width:620px;margin:32px auto;padding:0 16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:20px 22px;margin-bottom:16px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:17px;margin:0 0 10px}
p,li{color:var(--mut)}label{display:block;font-weight:600;margin:12px 0 4px}
input{width:100%;box-sizing:border-box;padding:9px 10px;border:1px solid var(--line);border-radius:6px;background:var(--bg);color:var(--fg);font:inherit}
small{color:var(--mut);display:block;margin-top:3px}
button{margin-top:18px;padding:10px 18px;border:0;border-radius:6px;background:var(--acc);color:#fff;font:inherit;font-weight:600;cursor:pointer}
.err{border-color:var(--bad);color:var(--bad)}.ok{border-color:var(--ok);color:var(--ok)}
a{color:var(--acc)}code{font-size:13px}
</style></head><body><main>
<h1>Remote Support Manager server</h1>
<p>Version {{.Version}}. This page only opens on this computer.</p>
{{if .Error}}<div class="card err" role="alert">{{.Error}}</div>{{end}}
{{if .Saved}}<div class="card ok" role="status">Saved. The server is restarting with the new settings.</div>{{end}}
{{if not .NeedsAdmin}}<div class="card"><h2>Status</h2>
{{if .St.ConsoleURL}}<p>Technicians sign in at <a href="{{.St.ConsoleURL}}" target="_blank" rel="noopener">{{.St.ConsoleURL}}</a></p>{{end}}
{{if .St.Listening}}<p>Listening on port <code>{{.St.Listening}}</code>.</p>{{end}}
{{if .St.CertNote}}<p>{{.St.CertNote}}</p>{{end}}
{{if .St.ActionURL}}<p><strong>One more step:</strong> <a href="{{.St.ActionURL}}" target="_blank" rel="noopener">open this page and allow it</a>, then come back and press Save again.</p>{{end}}
{{if .St.Problem}}<p class="err">Problem: {{.St.Problem}}</p>{{end}}
</div>{{end}}
<form method="post" action="/" class="card">
{{if .NeedsAdmin}}<h2>Welcome. Let's set up your server.</h2>
<p>Create the administrator account you'll use to sign in to the console. You'll also set up two-step sign-in with an authenticator app the first time you sign in.</p>
<label for="name">Your name</label><input id="name" name="name" value="{{.Name}}" autocomplete="name" required>
<label for="email">Your email</label><input id="email" name="email" type="email" value="{{.Email}}" autocomplete="username" required>
<label for="password">Password</label><input id="password" name="password" type="password" minlength="12" autocomplete="new-password" required><small>At least 12 characters.</small>
<label for="password2">Password again</label><input id="password2" name="password2" type="password" minlength="12" autocomplete="new-password" required>
{{else}}<h2>Settings</h2>{{end}}
<label for="org">Organisation name</label><input id="org" name="org" value="{{.S.OrgName}}" maxlength="80" required><small>Customers see this before they agree to a session.</small>
<fieldset style="border:0;padding:0;margin:16px 0 0"><legend style="font-weight:600">How customers reach this server</legend>
<label style="font-weight:400"><input type="radio" name="mode" value="tailscale" style="width:auto" {{if eq .Mode "tailscale"}}checked{{end}}> Over the internet with Tailscale Funnel (free, no router changes)</label>
<small>{{if .TSName}}This computer's Tailscale address is <code>{{.TSName}}</code>. Saving turns on Funnel for it.{{else}}Install Tailscale from tailscale.com, sign in with a free account, then reload this page.{{if .TSProblem}} ({{.TSProblem}}){{end}}{{end}}</small>
<label style="font-weight:400"><input type="radio" name="mode" value="router" style="width:auto" {{if eq .Mode "router"}}checked{{end}}> Over the internet with my own address and router</label>
<small>Free certificate from Let's Encrypt. Your router must forward port 443 to this computer.</small>
<label style="font-weight:400"><input type="radio" name="mode" value="cloudflare" style="width:auto" {{if eq .Mode "cloudflare"}}checked{{end}}> Over the internet with a Cloudflare Tunnel I've set up</label>
<small>Point the tunnel at https://127.0.0.1:8443 with certificate checks off (No TLS Verify).</small>
<label style="font-weight:400"><input type="radio" name="mode" value="local" style="width:auto" {{if eq .Mode "local"}}checked{{end}}> Only on my own network</label>
</fieldset>
<label for="host">Internet address (for the router and Cloudflare choices)</label><input id="host" name="host" value="{{.S.PublicHost}}" placeholder="yourname.duckdns.org" autocapitalize="off" spellcheck="false">
<label for="certEmail">Email for certificate notices (optional, router choice)</label><input id="certEmail" name="certEmail" type="email" value="{{.S.CertEmail}}">
{{if not .NeedsAdmin}}<p style="margin-top:18px">To save changes, confirm with an administrator account.</p>
<label for="email">Administrator email</label><input id="email" name="email" type="email" autocomplete="username" required>
<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>{{end}}
<button type="submit">{{if .NeedsAdmin}}Create account and start{{else}}Save{{end}}</button>
</form>
</main></body></html>`))
