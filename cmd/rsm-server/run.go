package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/TechSavage1989/remote-support-manager/internal/server"
	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
)

// runner is the no-command-line way to run the server: settings come from
// the local setup page (<data>/settings.json), the first administrator is
// created there, and HTTPS restarts whenever the settings change. The
// Windows service runs exactly this.
type runner struct {
	data      string
	setupAddr string // loopback only
	downloads string
	updates   string
	// Ports used when listening; tests override them.
	publicListen string // with an internet name (default ":443")
	httpListen   string // redirect and certificate checks (default ":80"; "-" turns it off)
	localListen  string // without an internet name (default ":8443")
	tunnelListen string // behind Tailscale or Cloudflare (default "127.0.0.1:8443")
	funnelOn     func(target string) error
	funnelOff    func() error
	tsName       func() (string, error)
	logf         func(string, ...any)

	mu     sync.Mutex
	status server.SetupStatus
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", "rsm-data", "data folder")
	setup := fs.String("setup", "127.0.0.1:8080", "loopback address of the setup page")
	downloads := fs.String("downloads", "", "client and viewer downloads folder (default: downloads next to rsm-server.exe)")
	updates := fs.String("updates", "", "signed update files to serve at /updates/")
	_ = fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRunner(*data, *setup, *downloads, *updates).run(ctx)
}

func newRunner(data, setup, downloads, updates string) *runner {
	if downloads == "" {
		downloads = defaultDownloads()
	}
	return &runner{data: data, setupAddr: setup, downloads: downloads, updates: updates,
		publicListen: ":443", httpListen: ":80", localListen: ":8443", tunnelListen: "127.0.0.1:8443",
		funnelOn: tailscaleFunnelOn, funnelOff: tailscaleFunnelOff, tsName: tailscaleName, logf: log.Printf}
}

func defaultDownloads() string {
	if exe, err := os.Executable(); err == nil {
		if d := filepath.Join(filepath.Dir(exe), "downloads"); isDir(d) {
			return d
		}
	}
	return ""
}

func (rn *runner) setStatus(f func(*server.SetupStatus)) {
	rn.mu.Lock()
	f(&rn.status)
	rn.mu.Unlock()
}

func (rn *runner) getStatus() server.SetupStatus {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.status
}

func (rn *runner) run(ctx context.Context) error {
	if !loopbackAddr(rn.setupAddr) {
		return fmt.Errorf("the setup page must listen on a loopback address, not %s", rn.setupAddr)
	}
	if err := os.MkdirAll(rn.data, 0o700); err != nil {
		return err
	}
	dbPath, _, _ := paths(rn.data)
	st, err := server.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	changed := make(chan struct{}, 1)
	page := &server.SetupPage{DataDir: rn.data, Store: st, Status: rn.getStatus, TailscaleName: rn.tsName,
		Changed: func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		}}
	ln, err := net.Listen("tcp", rn.setupAddr)
	if err != nil {
		return fmt.Errorf("setup page: %w", err)
	}
	setupSrv := &http.Server{Handler: page, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = setupSrv.Serve(ln) }()
	defer setupSrv.Close()
	rn.logf("Setup page: http://%s/", ln.Addr())

	for {
		users, _ := st.ListUsers()
		if len(users) == 0 {
			rn.setStatus(func(s *server.SetupStatus) {
				*s = server.SetupStatus{Problem: "", CertNote: "Waiting for the first administrator account."}
			})
			select {
			case <-ctx.Done():
				return nil
			case <-changed:
				continue
			}
		}
		set, err := server.LoadSettings(rn.data)
		if err != nil {
			rn.setStatus(func(s *server.SetupStatus) { s.Problem = "settings.json: " + err.Error() })
		}
		sctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- rn.serve(sctx, st, set) }()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-changed:
			cancel()
			<-done
		case err := <-done:
			cancel()
			msg := err.Error()
			if strings.Contains(msg, "address already in use") || strings.Contains(msg, "Only one usage") {
				msg += " (another program is using that port)"
			}
			rn.setStatus(func(s *server.SetupStatus) { s.Problem = msg })
			rn.logf("server stopped: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-changed:
			case <-time.After(30 * time.Second):
			}
		}
	}
}

// serve runs HTTPS with the given settings until ctx ends.
func (rn *runner) serve(ctx context.Context, st *server.Store, set server.Settings) error {
	cfg := server.Config{DataDir: rn.data, OrgName: set.OrgName, UpdatesDir: rn.updates, DownloadsDir: rn.downloads}
	var tlsCfg *tls.Config
	var listen, consoleURL, note string
	var m *autocert.Manager
	funnel := false
	switch {
	case set.PublicHost != "" && set.Tunnel != "":
		// The tunnel holds the public certificate and forwards to this
		// loopback listener; clients trust the public certificate, so no pin.
		_, certPath, keyPath := paths(rn.data)
		if _, err := ensureSelfSigned(certPath, keyPath); err != nil {
			return err
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return err
		}
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
		listen = rn.tunnelListen
		cfg.PublicURL = "https://" + set.PublicHost
		cfg.BehindTunnel = true
		consoleURL = cfg.PublicURL + "/"
		if set.Tunnel == "tailscale" {
			funnel = true
			note = "Turning on Tailscale Funnel for " + set.PublicHost + "…"
		} else {
			note = "Point your Cloudflare Tunnel at https://" + listen + " with No TLS Verify turned on."
		}
	case set.PublicHost != "":
		m = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(set.PublicHost),
			Cache:      autocert.DirCache(filepath.Join(rn.data, "certificates")),
			Email:      set.CertEmail,
		}
		tlsCfg = m.TLSConfig()
		tlsCfg.MinVersion = tls.VersionTLS12
		listen = rn.publicListen
		cfg.PublicURL = "https://" + set.PublicHost + portSuffix(listen, "443")
		consoleURL = cfg.PublicURL + "/"
		note = "Getting a free certificate from Let's Encrypt for " + set.PublicHost + "…"
	default:
		_, certPath, keyPath := paths(rn.data)
		pin, err := ensureSelfSigned(certPath, keyPath)
		if err != nil {
			return err
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return err
		}
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
		cfg.CertPin = pin
		listen = rn.localListen
		host, _ := os.Hostname()
		consoleURL = "https://" + strings.ToLower(host) + portSuffix(listen, "443") + "/"
		note = "Only on your own network: the server uses its own certificate, so browsers warn about it the first time."
	}
	srv := server.New(cfg, st)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", listen, err)
	}
	hs := &http.Server{Handler: srv, TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: log.New(os.Stderr, "http ", log.LstdFlags)}
	rn.setStatus(func(s *server.SetupStatus) {
		*s = server.SetupStatus{ConsoleURL: consoleURL, Listening: listen, CertNote: note}
	})
	var redirect *http.Server
	if m != nil && rn.httpListen != "-" {
		// Port 80 is optional: it lets people type the name without https://.
		if hl, err := net.Listen("tcp", rn.httpListen); err == nil {
			redirect = &http.Server{Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = redirect.Serve(hl) }()
		}
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.ServeTLS(ln, "", "") }()
	if m != nil {
		go rn.fetchCertificate(ctx, m, set.PublicHost)
	}
	if funnel && rn.funnelOn != nil {
		go rn.startFunnel(ln.Addr().String(), set.PublicHost)
	} else if set.Tunnel != "tailscale" && rn.funnelOff != nil && rn.hadFunnel() {
		_ = rn.funnelOff()
		rn.setFunnel(false)
	}
	rn.logf("Remote Support Manager %s: console %s", server.Version, consoleURL)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if redirect != nil {
			_ = redirect.Close()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if redirect != nil {
		_ = redirect.Shutdown(sctx)
	}
	_ = hs.Shutdown(sctx)
	return nil
}

// fetchCertificate asks for the certificate straight away instead of on the
// first visit, so the setup page can say whether it worked.
func (rn *runner) fetchCertificate(ctx context.Context, m *autocert.Manager, host string) {
	for attempt := 0; ; attempt++ {
		_, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: host, SupportedProtos: []string{"h2", "http/1.1"},
			CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12}})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			rn.setStatus(func(s *server.SetupStatus) {
				s.CertNote, s.Problem = "Certificate ready: customers and technicians can reach https://"+host+" from the internet.", ""
			})
			return
		}
		rn.setStatus(func(s *server.SetupStatus) {
			s.CertNote = "The certificate isn't ready yet. Check that " + host + " points to your internet address and that your router forwards port 443 to this computer. Retrying automatically."
			s.Problem = err.Error()
		})
		wait := time.Duration(1<<min(attempt, 5)) * time.Minute
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (rn *runner) startFunnel(target, host string) {
	err := rn.funnelOn(target)
	rn.setFunnel(err == nil)
	rn.setStatus(func(s *server.SetupStatus) {
		if err == nil {
			s.CertNote, s.Problem, s.ActionURL = "Tailscale Funnel is on: customers and technicians can reach https://"+host+" from the internet.", "", ""
			return
		}
		s.CertNote = "Tailscale Funnel isn't on yet."
		s.Problem, s.ActionURL = err.Error(), tailscaleLink(err.Error())
	})
}

func (rn *runner) hadFunnel() bool {
	b, _ := os.ReadFile(filepath.Join(rn.data, "funnel-on"))
	return len(b) > 0
}

func (rn *runner) setFunnel(on bool) {
	p := filepath.Join(rn.data, "funnel-on")
	if on {
		_ = os.WriteFile(p, []byte("1"), 0o600)
	} else {
		_ = os.Remove(p)
	}
}

func portSuffix(listen, def string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == def || port == "" {
		return ""
	}
	return ":" + port
}

func loopbackAddr(a string) bool {
	host, _, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ensureSelfSigned creates the local-network certificate on first use and
// returns its fingerprint.
func ensureSelfSigned(certPath, keyPath string) (string, error) {
	if _, err := os.Stat(certPath); err == nil {
		return tlspin.FileFingerprint(certPath)
	}
	hosts := []string{"localhost", "127.0.0.1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, strings.ToLower(h))
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				hosts = append(hosts, ipn.IP.String())
			}
		}
	}
	return tlspin.GenerateSelfSigned(certPath, keyPath, hosts, 825*24*time.Hour)
}
