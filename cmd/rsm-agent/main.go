// Command rsm-agent is the Remote Support Manager endpoint client.
//
// It connects outbound to the self-hosted server over pinned TLS and opens
// no listening ports. It always shows a tray icon while it runs and an
// amber bar at the top of the screen during a session. It runs in one of
// four ways, chosen by the technician when they create the download:
//
//   - attended: installed per user; every session needs the person at the
//     PC to accept and type the technician's 6-digit code.
//
//   - one-time (portable): nothing is installed. The person runs it, sees
//     who is asking and what they may do, and agrees once. It closes after
//     the session and the server forgets the PC.
//
//   - support: installed per user after the person agrees once. The
//     technician can reconnect for as long as needed; deleting the support
//     session in the console uninstalls it from the PC.
//
//   - unattended access: installed machine-wide by an administrator (UAC
//     prompt) as a Windows service; sessions start without a prompt but the
//     bar is still shown. Removing the device uninstalls it.
//
//     RemoteSupportAgent.exe                 (reads client.json next to it, or runs the installed client)
//     RemoteSupportAgent.exe enroll --server https://rsm.example.test:8443 --token rsm-enroll-... --pin <fingerprint>
//     RemoteSupportAgent.exe uninstall | uninstall-access | version
package main

import (
	"context"
	"encoding/json"
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
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
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
	case "join":
		err = cmdJoinFile(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "install-access":
		err = cmdInstallAccess(args)
	case "uninstall-access":
		err = cmdUninstallAccess(args)
	case "service":
		err = cmdService(args)
	case "session":
		err = cmdSessionHelper(args)
	case "notice":
		err = cmdNotice(args)
	case "version":
		fmt.Println(agentcore.Version)
	default:
		err = fmt.Errorf("unknown command %q (use run, enroll, uninstall, uninstall-access or version)", cmd)
	}
	if err != nil {
		messageBox(err.Error(), true)
		os.Exit(1)
	}
}

func logTo(dir string) *log.Logger {
	if dir == "" {
		return log.New(io.Discard, "", 0)
	}
	_ = os.MkdirAll(dir, 0o700)
	lp := filepath.Join(dir, "agent.log")
	if st, err := os.Stat(lp); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(lp, lp+".old")
	}
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	return log.New(f, "", log.LstdFlags)
}

func logger() *log.Logger {
	path, err := agentcore.ConfigPath()
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	return logTo(filepath.Dir(path))
}

// clientFileNextToExe reads client.json from the program's folder.
func clientFileNextToExe() (*protocol.ClientFile, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	return readClientFile(filepath.Join(filepath.Dir(exe), "client.json"))
}

func readClientFile(path string) (*protocol.ClientFile, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	var cf protocol.ClientFile
	if err := json.Unmarshal(b, &cf); err != nil || cf.Server == "" {
		return nil, path, errors.New("client.json is damaged; download the support program again")
	}
	if cf.Kind == "" {
		cf.Kind = protocol.KindOneTime
	}
	return &cf, path, nil
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
	p, cleanup, err := newPlatform(modeUser)
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
		// Not installed for this user: a download from the console has
		// client.json next to the program.
		if cf, cfPath, ferr := clientFileNextToExe(); ferr == nil {
			return joinWith(cf, cfPath)
		} else if !errors.Is(ferr, os.ErrNotExist) {
			return ferr
		}
		return errors.New("this PC is not set up for remote support yet. Download the support program from your technician's link and run it from the same folder as client.json.")
	}
	if confirmStartup() {
		return nil // an update failed to start; the previous version was restored and started
	}
	if !singleInstance() {
		return nil // already running for this user
	}
	p, cleanup, err := newPlatform(modeUser)
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
		// The technician deleted the support session, or an administrator
		// removed the device: uninstall from this user account.
		cleanup()
		removeUserInstall(true)
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdJoinFile(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	file := fs.String("file", "", "path to client.json (default: next to the program)")
	fs.BoolVar(&simulate, "simulate", false, "use a synthetic endpoint (non-Windows testing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var cf *protocol.ClientFile
	var path string
	var err error
	if *file != "" {
		cf, path, err = readClientFile(*file)
	} else {
		cf, path, err = clientFileNextToExe()
	}
	if err != nil {
		return err
	}
	return joinWith(cf, path)
}

// joinWith shows who is asking and what for, and sets the client up the
// way the technician's download says, once the person at the PC agrees.
func joinWith(cf *protocol.ClientFile, cfPath string) error {
	p, cleanup, err := newPlatform(modeUser)
	if err != nil {
		return err
	}
	defer cleanup()
	code := ""
	if cf.Invite == "" {
		var ok bool
		if code, ok = askCode(p, cf.Org); !ok {
			return nil
		}
	}
	var info *agentcore.InviteInfo
	for {
		info, err = agentcore.FetchInvite(cf.Server, cf.Pin, cf.Invite, code)
		if err == nil {
			break
		}
		if cf.Invite != "" {
			return fmt.Errorf("this support link can no longer be used (%v). Ask your technician for a new one.", err)
		}
		var ok bool
		if code, ok = askCode(p, cf.Org+"\n\nThat code was not accepted: "+err.Error()); !ok {
			return nil
		}
	}
	if info.Kind == protocol.KindAccess {
		// Unattended access is installed machine-wide by an administrator.
		return startAccessInstall(p, cf, cfPath, info)
	}
	granted, ok := askInvite(p, info)
	if !ok {
		return nil
	}
	switch info.Kind {
	case protocol.KindOneTime:
		cfg, err := agentcore.Join(p, cf.Server, cf.Pin, cf.Invite, code, granted, "")
		if err != nil {
			return err
		}
		return runOneTime(p, cfg)
	default: // support, attended
		path, err := agentcore.ConfigPath()
		if err != nil {
			return err
		}
		if _, err := agentcore.Join(p, cf.Server, cf.Pin, cf.Invite, code, granted, path); err != nil {
			return err
		}
		exe, err := install(true)
		if err != nil {
			return fmt.Errorf("set up, but installing failed: %w", err)
		}
		msg := "Remote support is set up for your user account. A tray icon shows while it runs."
		if info.Kind == protocol.KindSupport {
			msg = "Remote support is set up. " + info.Technician + " can connect while this support session is open; you will always see a bar at the top of the screen when they do.\n\nWhen your technician closes the support session, the program removes itself. You can also remove it any time from Settings > Apps."
		}
		cleanup()
		messageBox(msg, false)
		return startDetached(exe)
	}
}

// runOneTime runs the portable client until its session ends.
func runOneTime(p agentcore.Platform, cfg *agentcore.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	setQuit(p, stop)
	a := &agentcore.Agent{P: p, Cfg: cfg, Log: log.New(io.Discard, "", 0), SessionEnded: func(string) { stop() }}
	err := a.Run(ctx)
	if errors.Is(err, agentcore.ErrRevoked) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdUninstall(args []string) error {
	quiet := len(args) > 0 && args[0] == "--quiet"
	if !quiet && !confirm("Remove the Remote Support Manager agent from this user account?\n\nThis deletes its settings and credential; technicians will no longer be able to connect to this PC.") {
		return nil
	}
	removeUserInstall(false)
	if !quiet {
		messageBox("The agent was removed.", false)
	}
	return nil
}
