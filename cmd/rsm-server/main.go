// Command rsm-server runs the self-hostable Remote Support Manager server.
//
//	rsm-server init  --data DIR --hosts rsm.example.test,192.0.2.10 --admin-email a@example.com --admin-name "Alex Admin"
//	rsm-server serve --data DIR --listen :8443
//	rsm-server fingerprint --data DIR
//	rsm-server reset-mfa --data DIR --email a@example.com
//
// The server only ever listens with TLS (the loopback-only setup page of
// "run" is the one plain-HTTP exception). Only when an internet address is
// chosen on the setup page does it contact Let's Encrypt for a certificate,
// or turn on Tailscale Funnel. Opening firewall ports and registering the
// Windows service happen only in the installer (RemoteSupportServerSetup.exe).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/server"
	"github.com/TechSavage1989/remote-support-manager/internal/tlspin"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: rsm-server <run|init|serve|fingerprint|reset-mfa|version> [flags]
  run        no-command-line mode: settings and the first administrator come from
             the setup page at http://127.0.0.1:8080 (what the Windows service runs)
run "rsm-server <command> -h" for flags`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "serve":
		err = cmdServe(args)
	case "run":
		err = cmdRun(args)
	case "service":
		err = cmdService(args)
	case "install":
		err = cmdInstall(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "fingerprint":
		err = cmdFingerprint(args)
	case "reset-mfa":
		err = cmdResetMFA(args)
	case "version":
		fmt.Println(server.Version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func paths(data string) (db, cert, key string) {
	return filepath.Join(data, "rsm.db"), filepath.Join(data, "server.crt"), filepath.Join(data, "server.key")
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	data := fs.String("data", "rsm-data", "data folder (database, certificate, key)")
	hosts := fs.String("hosts", "localhost,127.0.0.1", "comma-separated host names and IP addresses for the self-signed certificate")
	email := fs.String("admin-email", "", "first administrator's email")
	name := fs.String("admin-name", "Administrator", "first administrator's name")
	_ = fs.Parse(args)
	if *email == "" || !strings.Contains(*email, "@") {
		return errors.New("--admin-email is required")
	}
	if err := os.MkdirAll(*data, 0o700); err != nil {
		return err
	}
	dbPath, certPath, keyPath := paths(*data)
	if _, err := os.Stat(dbPath); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", dbPath)
	}
	var pin string
	if _, err := os.Stat(certPath); err == nil {
		pin, err = tlspin.FileFingerprint(certPath)
		if err != nil {
			return err
		}
		fmt.Println("Using existing certificate", certPath)
	} else {
		hl := []string{}
		for _, h := range strings.Split(*hosts, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hl = append(hl, h)
			}
		}
		pin, err = tlspin.GenerateSelfSigned(certPath, keyPath, hl, 825*24*time.Hour)
		if err != nil {
			return err
		}
		fmt.Println("Created self-signed certificate for", strings.Join(hl, ", "))
	}
	st, err := server.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	pw := "Rsm-" + server.RandomToken(9)
	h, err := server.HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(*email, *name, h, true, true); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Administrator:          ", *email)
	fmt.Println("One-time password:      ", pw, "(shown once; change it after signing in)")
	fmt.Println("Certificate fingerprint:", pin)
	fmt.Println()
	fmt.Println("Next: rsm-server serve --data", *data)
	return nil
}

func cmdFingerprint(args []string) error {
	fs := flag.NewFlagSet("fingerprint", flag.ExitOnError)
	data := fs.String("data", "rsm-data", "data folder")
	cert := fs.String("cert", "", "certificate file (default: <data>/server.crt)")
	_ = fs.Parse(args)
	_, c, _ := paths(*data)
	if *cert != "" {
		c = *cert
	}
	pin, err := tlspin.FileFingerprint(c)
	if err != nil {
		return err
	}
	fmt.Println(pin)
	return nil
}

func cmdResetMFA(args []string) error {
	fs := flag.NewFlagSet("reset-mfa", flag.ExitOnError)
	data := fs.String("data", "rsm-data", "data folder")
	email := fs.String("email", "", "user whose MFA should be reset (they enrol again at next sign-in)")
	_ = fs.Parse(args)
	dbPath, _, _ := paths(*data)
	st, err := server.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	u, err := st.UserByEmail(*email)
	if err != nil {
		return fmt.Errorf("no user %q", *email)
	}
	if err := st.SetTOTP(u.ID, ""); err != nil {
		return err
	}
	st.Audit(server.AuditEntry{Actor: "local-cli", Action: "user.mfa_reset", Details: map[string]any{"email": u.Email}})
	fmt.Println("MFA reset for", u.Email)
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	data := fs.String("data", "rsm-data", "data folder")
	listen := fs.String("listen", "127.0.0.1:8443", "address to listen on (TLS only); use 0.0.0.0:8443 only on a network you control")
	cert := fs.String("cert", "", "certificate (default: <data>/server.crt)")
	key := fs.String("key", "", "private key (default: <data>/server.key)")
	org := fs.String("org", "Remote Support", "organisation name shown to endpoint users")
	updates := fs.String("updates", "", "folder with signed update manifests and packages to serve at /updates/")
	grace := fs.Duration("reconnect-grace", 60*time.Second, "how long a dropped session waits for a reconnect")
	downloads := fs.String("downloads", "", "folder with RemoteSupportAgent.exe and the viewer files offered in the console (default: downloads next to rsm-server.exe, if present)")
	public := fs.String("public-url", "", "https:// address clients use to reach this server (default: the address the browser used)")
	_ = fs.Parse(args)
	if *downloads == "" {
		if exe, err := os.Executable(); err == nil {
			if d := filepath.Join(filepath.Dir(exe), "downloads"); isDir(d) {
				*downloads = d
			}
		}
	}
	if *public != "" && !strings.HasPrefix(*public, "https://") {
		return errors.New("--public-url must start with https://")
	}
	dbPath, c, k := paths(*data)
	if *cert != "" {
		c = *cert
	}
	if *key != "" {
		k = *key
	}
	pin, err := tlspin.FileFingerprint(c)
	if err != nil {
		return fmt.Errorf("certificate: %w (run rsm-server init first)", err)
	}
	st, err := server.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	srv := server.New(server.Config{DataDir: *data, OrgName: *org, UpdatesDir: *updates, DownloadsDir: *downloads, PublicURL: strings.TrimRight(*public, "/"), ReconnectGrace: *grace, CertPin: pin}, st)
	pair, err := tls.LoadX509KeyPair(c, k)
	if err != nil {
		return err
	}
	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}},
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(os.Stderr, "http ", log.LstdFlags),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Printf("Remote Support Manager %s listening on https://%s (certificate fingerprint %s)", server.Version, *listen, pin)
	if err := hs.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
