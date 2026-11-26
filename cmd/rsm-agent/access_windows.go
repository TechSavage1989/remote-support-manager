//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/TechSavage1989/remote-support-manager/internal/agentcore"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

// Unattended access: an administrator installs the client machine-wide.
// A LocalSystem service keeps the outbound connection; for each session it
// starts a helper process in the console session that captures the screen,
// shows the amber bar and follows the sign-in and UAC desktops. A tray
// notice runs for every signed-in user. Removing the device in the console
// makes the service uninstall everything it added.

const (
	serviceName   = "RemoteSupportManagerAccess"
	noticeRunName = "RemoteSupportManagerAccessNotice"
	accessUninst  = "RemoteSupportManagerAccess"
	accessApp     = "Remote Support Manager Access"
)

type accessInfo struct {
	Org     string `json:"org"`
	Server  string `json:"server"`
	Label   string `json:"label"`
	PrevSAS int    `json:"prevSAS"`
}

func accessDirs() (prog, data string) {
	return winapi.ProgramFilesDir(accessApp), winapi.ProgramDataDir(accessApp)
}

func accessConfigPath() string { _, d := accessDirs(); return filepath.Join(d, "agent.json") }

// startAccessInstall runs in the person's own (unelevated) process: it
// explains unattended access, asks for confirmation and then starts the
// installer through the Windows administrator prompt.
func startAccessInstall(ap agentcore.Platform, cf *protocol.ClientFile, cfPath string, info *agentcore.InviteInfo) error {
	if _, ok := askInvite(ap, info); !ok {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if winapi.IsElevated() {
		return installAccess(ap.(*winPlatform), cf, info.Label)
	}
	return winapi.RunElevated(exe, fmt.Sprintf(`install-access --file "%s" --confirmed`, cfPath))
}

func cmdInstallAccess(args []string) error {
	fs := flag.NewFlagSet("install-access", flag.ContinueOnError)
	file := fs.String("file", "", "path to client.json")
	confirmed := fs.Bool("confirmed", false, "the person already confirmed in the invitation window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !winapi.IsElevated() {
		return errors.New("installing unattended access needs administrator rights")
	}
	cf, _, err := readClientFile(*file)
	if err != nil {
		return err
	}
	ap, cleanup, err := newPlatform(modeUser)
	if err != nil {
		return err
	}
	defer cleanup()
	info, err := agentcore.FetchInvite(cf.Server, cf.Pin, cf.Invite, "")
	if err != nil {
		return fmt.Errorf("this installer can no longer be used (%v). Ask your administrator for a new one.", err)
	}
	if info.Kind != protocol.KindAccess {
		return errors.New("this download is not for unattended access")
	}
	if !*confirmed {
		if _, ok := askInvite(ap, info); !ok {
			return nil
		}
	}
	return installAccess(ap.(*winPlatform), cf, info.Label)
}

func installAccess(p *winPlatform, cf *protocol.ClientFile, label string) (err error) {
	prog, data := accessDirs()
	if err := os.MkdirAll(prog, 0o755); err != nil {
		return err
	}
	if err := winapi.SecureDir(data); err != nil {
		return fmt.Errorf("cannot create the settings folder: %w", err)
	}
	dst := filepath.Join(prog, exeName)
	src, _ := os.Executable()
	if !strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(data)
			_ = os.RemoveAll(prog)
		}
	}()
	// Enrol with a machine-scope credential (readable only by SYSTEM and
	// administrators through the folder permissions).
	p.mode = modeHelper
	_, err = agentcore.Join(p, cf.Server, cf.Pin, cf.Invite, "", nil, accessConfigPath())
	p.mode = modeUser
	if err != nil {
		return err
	}
	prev, _ := winapi.EnableServiceSAS()
	ai := accessInfo{Org: cf.Org, Server: cf.Server, Label: label, PrevSAS: prev}
	b, _ := json.MarshalIndent(ai, "", "  ")
	if err = os.WriteFile(filepath.Join(prog, "access.json"), b, 0o644); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if old, oerr := m.OpenService(serviceName); oerr == nil {
		_, _ = old.Control(svc.Stop)
		_ = old.Delete()
		old.Close()
		time.Sleep(2 * time.Second)
	}
	s, err := m.CreateService(serviceName, `"`+dst+`" service`, mgr.Config{
		DisplayName: appTitle + " (unattended access)",
		Description: "Keeps an outbound connection to " + cf.Server + " so administrators of " + cf.Org + " can connect. Remove it from Settings > Apps.",
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("cannot create the service: %w", err)
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second}, {Type: mgr.ServiceRestart, Delay: time.Minute}}, 86400)
	if err = s.Start(); err != nil {
		_ = s.Delete()
		return fmt.Errorf("cannot start the service: %w", err)
	}
	_ = winapi.SetRunAtSignInAllUsers(noticeRunName, `"`+dst+`" notice`, true)
	_ = winapi.AddUninstallEntryMachine(accessUninst, appTitle+" (unattended access)", agentcore.Version, dst, `"`+dst+`" uninstall-access`)
	_ = startDetached(dst, "notice")
	messageBox("Unattended access is installed. Administrators of "+cf.Org+" can now connect to this PC. A tray notice is shown to everyone who signs in, and a bar appears at the top of the screen during sessions.\n\nTo remove it, use Settings > Apps > "+appTitle+" (unattended access).", false)
	return nil
}

func cmdUninstallAccess(args []string) error {
	quiet := len(args) > 0 && args[0] == "--quiet"
	if !winapi.IsElevated() {
		exe, _ := os.Executable()
		return winapi.RunElevated(exe, "uninstall-access")
	}
	if !quiet && !confirm("Remove unattended access from this PC?\n\nAdministrators will no longer be able to connect. The device stays listed as offline in their console until they remove it.") {
		return nil
	}
	removeAccess(false)
	if !quiet {
		messageBox("Unattended access was removed from this PC.", false)
	}
	return nil
}

// removeAccess undoes everything installAccess added. fromService is true
// when the service itself is uninstalling after the device was removed.
func removeAccess(fromService bool) {
	prog, data := accessDirs()
	var ai accessInfo
	if b, err := os.ReadFile(filepath.Join(prog, "access.json")); err == nil {
		_ = json.Unmarshal(b, &ai)
	} else {
		ai.PrevSAS = -1
	}
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(serviceName); err == nil {
			if !fromService {
				_, _ = s.Control(svc.Stop)
			}
			_ = s.Delete() // removed once it stops
			s.Close()
		}
		m.Disconnect()
	}
	_ = winapi.SetRunAtSignInAllUsers(noticeRunName, "", false)
	winapi.RemoveUninstallEntryMachine(accessUninst)
	winapi.RestoreServiceSAS(ai.PrevSAS)
	_ = os.RemoveAll(data)
	selfDelete(prog)
}

// ------------------------------------------------------------ service

type accessService struct{}

func cmdService(args []string) error {
	in, err := svc.IsWindowsService()
	if err != nil || !in {
		return errors.New("this command is run by Windows as the unattended-access service")
	}
	return svc.Run(serviceName, accessService{})
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func (accessService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	_, data := accessDirs()
	lg := logTo(data)
	cfg, err := agentcore.LoadConfig(accessConfigPath())
	if err != nil {
		lg.Printf("no configuration: %v", err)
		return true, 1
	}
	p, _, _ := newPlatform(modeService)
	exe, _ := os.Executable()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &agentcore.Agent{P: p, Cfg: cfg, Log: lg, Delegate: func(id string) bool {
		if !sessionIDRe.MatchString(id) {
			return false
		}
		_, err := winapi.StartInConsoleSession(exe, fmt.Sprintf(`"%s" session --id %s`, exe, id))
		if err != nil {
			lg.Printf("session helper for %s: %v", id, err)
			return false
		}
		return true
	}}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			if errors.Is(err, agentcore.ErrRevoked) {
				lg.Printf("device removed by the server; uninstalling")
				st <- svc.Status{State: svc.StopPending}
				removeAccess(true)
				return false, 0
			}
			lg.Printf("stopped: %v", err)
			return true, 2
		}
	}
}

// cmdSessionHelper runs one unattended session in the console session.
func cmdSessionHelper(args []string) error {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	id := fs.String("id", "", "session id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !winapi.IsLocalSystem() || !sessionIDRe.MatchString(*id) {
		return errors.New("this command is started by the unattended-access service")
	}
	cfg, err := agentcore.LoadConfig(accessConfigPath())
	if err != nil {
		return err
	}
	p, cleanup, err := newPlatform(modeHelper)
	if err != nil {
		return err
	}
	defer cleanup()
	_, data := accessDirs()
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	a := &agentcore.Agent{P: p, Cfg: cfg, Log: logTo(data)}
	return a.RunSession(ctx, *id)
}

// ------------------------------------------------------------ tray notice

// cmdNotice shows a tray icon for every signed-in user while unattended
// access is installed. It exits once the service is gone.
func cmdNotice(args []string) error {
	name, _ := windows.UTF16PtrFromString(`Local\RemoteSupportManagerAccessNotice`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return nil
	}
	prog, _ := accessDirs()
	var ai accessInfo
	if b, err := os.ReadFile(filepath.Join(prog, "access.json")); err == nil {
		_ = json.Unmarshal(b, &ai)
	}
	if ai.Org == "" {
		ai.Org = "your organisation"
	}
	winapi.EnableDPIAwareness()
	var tray *winapi.Tray
	ui := winapi.NewUI(func(ui *winapi.UI) {
		winapi.RegisterClass("RSMAccessNotice", 0)
		owner := winapi.CreateWindow(0, "RSMAccessNotice", appTitle, 0, 0, 0, 0, 0, 0, 0, func(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
			if msg == winapi.TrayMessage && (lp == winapi.WM_LBUTTONUP || lp == winapi.WM_RBUTTONUP) {
				winapi.MessageBox(h, "Unattended access is enabled on this PC.\n\nAdministrators of "+ai.Org+" can connect to it through "+ai.Server+". A bar is shown at the top of the screen whenever someone is connected.\n\nAn administrator can remove it from Settings > Apps > "+appTitle+" (unattended access).", appTitle, winapi.MB_ICONINFORMATION)
				return 0, true
			}
			return 0, false
		})
		tray = winapi.NewTray(owner, appTitle+": unattended access enabled for "+ai.Org)
	})
	for {
		time.Sleep(15 * time.Second)
		if !serviceInstalled() {
			ui.Do(func() { tray.Remove() })
			return nil
		}
	}
}

func serviceInstalled() bool {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return true // cannot tell; keep showing the notice
	}
	defer windows.CloseServiceHandle(h)
	n, _ := windows.UTF16PtrFromString(serviceName)
	s, err := windows.OpenService(h, n, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
	}
	windows.CloseServiceHandle(s)
	return true
}
