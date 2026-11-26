//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

const title = "Remote Support Manager Server setup"

func main() {
	winapi.EnableDPIAwareness()
	if err := run(); err != nil {
		winapi.MessageBox(0, err.Error(), title, winapi.MB_ICONWARNING)
		os.Exit(1)
	}
}

func run() error {
	if !hasServer() {
		return errors.New("this setup was built without the server; use scripts/build-windows.sh or build-windows.ps1")
	}
	msg := "Install Remote Support Manager Server " + Version + " on this computer?\n\n" +
		"It runs in the background as a Windows service so technicians and customers can connect to it. " +
		"This computer needs to stay on.\n\n" +
		"Windows will ask for administrator permission next. After that, a setup page opens in your browser."
	if winapi.MessageBox(0, msg, title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) != winapi.IDYES {
		return nil
	}
	dir, err := os.MkdirTemp("", "rsm-server-setup-")
	if err != nil {
		return err
	}
	if err := unpack(dir); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	exe := filepath.Join(dir, "rsm-server.exe")
	if err := winapi.RunElevated(exe, `install --from "`+dir+`"`); err != nil {
		_ = os.RemoveAll(dir)
		return errors.New("setup needs administrator permission to install the server")
	}
	return nil
}
