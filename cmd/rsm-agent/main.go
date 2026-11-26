// Command rsm-agent is the Remote Support Manager endpoint agent.
//
// It runs as the signed-in Windows user, connects outbound to the
// self-hosted server over authenticated TLS, opens no listening ports and
// only shares the screen after the person at the PC accepts a request and
// types the technician's support code. It shows a tray icon at all times
// and a bar at the top of the screen during a session.
//
//	RemoteSupportAgent.exe enroll --server https://rsm.example.test:8443 --token rsm-enroll-... --pin <fingerprint>
//	RemoteSupportAgent.exe            (run; started at sign-in after install)
//	RemoteSupportAgent.exe uninstall
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
)

var simulate bool

func hostname() (string, error) { return os.Hostname() }

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "enroll":
		err = cmdEnroll(args)
	case "run":
		err = cmdRun(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "version":
		fmt.Println(agentcore.Version)
	default:
		err = fmt.Errorf("unknown command %q (use enroll, run, uninstall or version)", cmd)
	}
	if err != nil {
		messageBox(err.Error(), true)
		os.Exit(1)
	}
}

func logger() *log.Logger {
	path, err := agentcore.ConfigPath()
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	lp := filepath.Join(filepath.Dir(path), "agent.log")
	if st, err := os.Stat(lp); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(lp, lp+".old")
	}
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	return log.New(f, "", log.LstdFlags)
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	server := fs.String("server", "", "server URL, e.g. https://rsm.example.test:8443")
	token := fs.String("token", "", "one-time enrollment token from the console")
	pin := fs.String("pin", "", "server certificate fingerprint (required for self-signed certificates)")
	noStart := fs.Bool("no-autostart", false, "do not start the agent at sign-in")
	fs.BoolVar(&simulate, "simulate", false, "use a synthetic endpoint (non-Windows testing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *token == "" {
		return errors.New("enroll needs --server and --token")
	}
	if !confirm(fmt.Sprintf("Enroll this PC with %s?\n\nTechnicians in that organisation will be able to REQUEST remote support. Nothing is shared until you accept a request and type their support code. A tray icon shows the agent is running, and you can uninstall it from Settings > Apps.", *server)) {
		return errors.New("enrollment cancelled")
	}
	p, cleanup, err := newPlatform()
	if err != nil {
		return err
	}
	defer cleanup()
	path, err := agentcore.ConfigPath()
	if err != nil {
		return err
	}
	if _, err := agentcore.Enroll(p, *server, *pin, *token, path); err != nil {
		return err
	}
	exe, err := install(!*noStart)
	if err != nil {
		return fmt.Errorf("enrolled, but installing failed: %w", err)
	}
	messageBox("This PC is enrolled. The agent is installed for your user account at:\n"+exe+"\n\nIt starts at sign-in and only connects outbound to your support server.", false)
	return startDetached(exe)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.BoolVar(&simulate, "simulate", false, "use a synthetic endpoint (non-Windows testing)")
	cfgFlag := fs.String("config", "", "settings file (default: per-user app data)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *cfgFlag
	if path == "" {
		var err error
		if path, err = agentcore.ConfigPath(); err != nil {
			return err
		}
	}
	cfg, err := agentcore.LoadConfig(path)
	if err != nil {
		return errors.New("this PC is not enrolled yet; run: RemoteSupportAgent.exe enroll --server <url> --token <token> --pin <fingerprint>")
	}
	if confirmStartup() {
		return nil // an update failed to start; the previous version was restored and started
	}
	if !singleInstance() {
		return nil // already running for this user
	}
	p, cleanup, err := newPlatform()
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	setQuit(p, stop)
	a := &agentcore.Agent{P: p, Cfg: cfg, CfgPath: path, Log: logger()}
	setUpdater(p, a, stop)
	markHealthy()
	err = a.Run(ctx)
	if errors.Is(err, agentcore.ErrRevoked) {
		_ = removeAutostart()
		return err
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdUninstall(args []string) error {
	quiet := len(args) > 0 && args[0] == "--quiet"
	if !quiet && !confirm("Remove the Remote Support Manager agent from this user account?\n\nThis deletes its settings and credential; technicians will no longer be able to request support for this PC.") {
		return nil
	}
	if path, err := agentcore.ConfigPath(); err == nil {
		_ = os.Remove(path)
	}
	if err := uninstall(); err != nil {
		return err
	}
	if !quiet {
		messageBox("The agent was removed. If it is still running, it stops at the next sign-in or when you choose Stop from its tray icon.", false)
	}
	return nil
}
