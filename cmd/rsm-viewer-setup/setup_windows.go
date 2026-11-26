//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

// Keep these in step with cmd/rsm-viewer (uninstallViewer).
const (
	title       = "Remote Support Manager Viewer setup"
	installDir  = "Remote Support Manager Viewer"
	uninstallID = "RemoteSupportManagerViewer"
	shortcut    = "Remote Support Manager Viewer.lnk"
)

func main() {
	winapi.EnableDPIAwareness()
	quiet := len(os.Args) > 1 && os.Args[1] == "/quiet"
	if err := run(quiet); err != nil {
		winapi.MessageBox(0, err.Error(), title, winapi.MB_ICONWARNING)
		os.Exit(1)
	}
}

func run(quiet bool) error {
	b := viewerBytes()
	if b == nil {
		return errors.New("this installer was built without the viewer; use scripts/build-windows.sh or build-windows.ps1")
	}
	dir := winapi.ProgramsDir(installDir)
	dst := filepath.Join(dir, viewerExe)
	if !quiet && winapi.MessageBox(0, "Install Remote Support Manager Viewer "+Version+" for your Windows account?\n\nIt is copied to:\n"+dir+"\n\nNo administrator rights are needed. You can remove it from Settings > Apps.", title, winapi.MB_YESNO|winapi.MB_ICONQUESTION) != winapi.IDYES {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return errors.New("could not replace the installed viewer; close it and run setup again")
	}
	_ = exec.Command(dst, "--register-quiet").Run()
	if sm := winapi.StartMenuDir(); sm != "" {
		_ = winapi.CreateShortcut(filepath.Join(sm, shortcut), dst, "Remote Support Manager technician viewer")
	}
	if err := winapi.AddUninstallEntry(uninstallID, "Remote Support Manager Viewer", Version, dst, `"`+dst+`" --uninstall`); err != nil {
		return err
	}
	if !quiet {
		winapi.MessageBox(0, "Remote Support Manager Viewer is installed. Find it in the Start menu; \"Open in viewer\" links from the console now open it.", title, winapi.MB_ICONINFORMATION)
		_ = exec.Command(dst).Start()
	}
	return nil
}
