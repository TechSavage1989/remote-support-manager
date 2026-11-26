//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/TechSavage1989/remote-support-manager/internal/server"
	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

// The Windows server install, done by RemoteSupportServerSetup.exe through
// the administrator prompt: rsm-server.exe and the downloads folder go to
// Program Files, data to ProgramData (SYSTEM and administrators only), a
// service runs "rsm-server run", Windows Firewall gets one rule for this
// program, and the Start menu gets a link to the local setup page.

const (
	svcName     = "RemoteSupportManagerServer"
	svcDisplay  = "Remote Support Manager Server"
	appDir      = "Remote Support Manager Server"
	uninstID    = "RemoteSupportManagerServer"
	fwRule      = "Remote Support Manager Server"
	setupURL    = "http://127.0.0.1:8080/"
	setupLinkNm = "Remote Support Manager Server setup.url"
)

func installedDirs() (prog, data string) {
	return winapi.ProgramFilesDir(appDir), winapi.ProgramDataDir(appDir)
}

// cmdService is what the Windows service runs.
func cmdService(args []string) error {
	in, err := svc.IsWindowsService()
	if err != nil || !in {
		return errors.New("this command is started by Windows as a service")
	}
	_, data := installedDirs()
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	d := fs.String("data", data, "data folder")
	_ = fs.Parse(args)
	if f, err := os.OpenFile(filepath.Join(*d, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(f)
		os.Stderr = f
		defer f.Close()
	}
	return svc.Run(svcName, &serverService{data: *d})
}

type serverService struct{ data string }

func (s *serverService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newRunner(s.data, "127.0.0.1:8080", "", "").run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				log.Printf("server stopped: %v", err)
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				return false, 0
			}
		}
	}
}

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	user32            = windows.NewLazySystemDLL("user32.dll")
	pGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	pShowWindow       = user32.NewProc("ShowWindow")
)

// hideConsole hides the console window Windows opens for rsm-server.exe
// when the installer or Settings > Apps starts it; people only see
// message boxes and the browser.
func hideConsole() {
	if h, _, _ := pGetConsoleWindow.Call(); h != 0 {
		_, _, _ = pShowWindow.Call(h, 0)
	}
}

// cmdInstall copies the program from --from (the setup's unpacked files) and
// registers everything. It needs administrator rights.
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	from := fs.String("from", "", "folder holding rsm-server.exe and downloads\\")
	quiet := fs.Bool("quiet", false, "no message boxes or browser")
	_ = fs.Parse(args)
	if !*quiet {
		hideConsole()
	}
	err := install(*from)
	if *from != "" && strings.HasPrefix(strings.ToLower(filepath.Clean(*from)), strings.ToLower(filepath.Clean(os.TempDir()))) {
		winapi.RemoveDirLater(*from)
	}
	if *quiet {
		return err
	}
	if err != nil {
		winapi.MessageBox(0, "Setup could not finish:\n\n"+err.Error(), svcDisplay+" setup", winapi.MB_ICONWARNING)
		return err
	}
	winapi.MessageBox(0, svcDisplay+" is installed and running.\n\nYour browser now opens the setup page, where you create your administrator account and choose how customers reach the server.\n\nYou can open it again from the Start menu: \"Remote Support Manager Server setup\".", svcDisplay+" setup", winapi.MB_ICONINFORMATION)
	winapi.OpenURL(setupURL)
	return nil
}

func install(from string) error {
	if !winapi.IsElevated() {
		return errors.New("installing the server needs administrator rights")
	}
	if from == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		from = filepath.Dir(exe)
	}
	prog, data := installedDirs()
	if err := winapi.SecureDir(data); err != nil {
		return fmt.Errorf("cannot create the data folder: %w", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	// An existing install is upgraded in place: stop, replace files, start.
	if old, err := m.OpenService(svcName); err == nil {
		_, _ = old.Control(svc.Stop)
		waitStopped(old)
		old.Close()
	}
	if err := os.MkdirAll(prog, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(prog, "rsm-server.exe")
	if err := copyFile(filepath.Join(from, "rsm-server.exe"), dst); err != nil {
		return fmt.Errorf("copying rsm-server.exe: %w", err)
	}
	if src := filepath.Join(from, "downloads"); isDir(src) {
		if err := copyDir(src, filepath.Join(prog, "downloads")); err != nil {
			return fmt.Errorf("copying downloads: %w", err)
		}
	}
	s, err := m.OpenService(svcName)
	if err != nil {
		s, err = m.CreateService(svcName, dst, mgr.Config{
			DisplayName: svcDisplay,
			Description: "Self-hosted Remote Support Manager server and web console. Settings: " + setupURL,
			StartType:   mgr.StartAutomatic,
		}, "service", "--data", data)
		if err != nil {
			return fmt.Errorf("creating the service: %w", err)
		}
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second}, {Type: mgr.ServiceRestart, Delay: time.Minute}}, 86400)
	// One inbound rule for this program only (ports 443, 80 and 8443 it
	// listens on). Customers' PCs never need inbound rules.
	_ = netsh("advfirewall", "firewall", "delete", "rule", "name="+fwRule)
	if err := netsh("advfirewall", "firewall", "add", "rule", "name="+fwRule, "dir=in", "action=allow", "protocol=TCP",
		"localport=80,443,8443", "program="+dst, "enable=yes"); err != nil {
		log.Printf("firewall rule: %v", err)
	}
	if sm := winapi.KnownFolder(windows.FOLDERID_CommonPrograms); sm != "" {
		_ = os.WriteFile(filepath.Join(sm, setupLinkNm), []byte("[InternetShortcut]\r\nURL="+setupURL+"\r\n"), 0o644)
	}
	if err := winapi.AddUninstallEntryMachine(uninstID, svcDisplay, server.Version, dst, `"`+dst+`" uninstall`); err != nil {
		return err
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	return nil
}

func waitStopped(s *mgr.Service) {
	for i := 0; i < 30; i++ {
		q, err := s.Query()
		if err != nil || q.State == svc.Stopped {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// cmdUninstall is the Settings > Apps uninstall command.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	quiet := fs.Bool("quiet", false, "no questions; keeps the data folder")
	_ = fs.Parse(args)
	if !*quiet {
		hideConsole()
	}
	if !winapi.IsElevated() {
		exe, _ := os.Executable()
		return winapi.RunElevated(exe, strings.Join(append([]string{"uninstall"}, args...), " "))
	}
	prog, data := installedDirs()
	if !*quiet && winapi.MessageBox(0, "Remove "+svcDisplay+" from this computer?\n\nTechnicians and customers will no longer be able to connect to it.", svcDisplay, winapi.MB_YESNO|winapi.MB_ICONQUESTION) != winapi.IDYES {
		return nil
	}
	deleteData := !*quiet && winapi.MessageBox(0, "Also delete the server's data (accounts, devices, audit log and certificates)?\n\nChoose No to keep it for a later reinstall. It stays in:\n"+data, svcDisplay, winapi.MB_YESNO|winapi.MB_ICONQUESTION) == winapi.IDYES
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(svcName); err == nil {
			_, _ = s.Control(svc.Stop)
			waitStopped(s)
			_ = s.Delete()
			s.Close()
		}
		m.Disconnect()
	}
	if _, err := os.Stat(filepath.Join(data, "funnel-on")); err == nil {
		_ = tailscaleFunnelOff()
	}
	_ = netsh("advfirewall", "firewall", "delete", "rule", "name="+fwRule)
	if sm := winapi.KnownFolder(windows.FOLDERID_CommonPrograms); sm != "" {
		_ = os.Remove(filepath.Join(sm, setupLinkNm))
	}
	winapi.RemoveUninstallEntryMachine(uninstID)
	if deleteData {
		_ = os.RemoveAll(data)
	}
	winapi.RemoveDirLater(prog)
	if !*quiet {
		winapi.MessageBox(0, svcDisplay+" was removed.", svcDisplay, winapi.MB_ICONINFORMATION)
	}
	return nil
}

func netsh(args ...string) error {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "netsh.exe"), args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		return copyFile(p, t)
	})
}
