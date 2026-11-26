//go:build windows

package winapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// This file holds the calls used only by unattended access: machine-wide
// install, the session helper and following the input desktop (sign-in
// screen, UAC prompts, lock screen).

var (
	pOpenInputDesktop         = user32.NewProc("OpenInputDesktop")
	pSetThreadDesktop         = user32.NewProc("SetThreadDesktop")
	pCloseDesktop             = user32.NewProc("CloseDesktop")
	pGetUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
)

// IsElevated reports whether this process runs with administrator rights.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// IsLocalSystem reports whether this process runs as LocalSystem.
func IsLocalSystem() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	sys, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	return err == nil && windows.EqualSid(u.User.Sid, sys)
}

// RunElevated starts exe with args through the normal Windows elevation
// prompt (the person at the PC must approve it).
func RunElevated(exe string, args string) error {
	return windows.ShellExecute(0, W("runas"), W(exe), W(args), nil, windows.SW_SHOWNORMAL)
}

// ProtectMachine encrypts data with DPAPI so that only processes on this
// computer can read it; the file is protected by NTFS permissions on the
// ProgramData folder (administrators and SYSTEM only).
func ProtectMachine(b []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, W("Remote Support Manager"), nil, 0, nil, 0x1|0x4 /* UI_FORBIDDEN|LOCAL_MACHINE */, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// ProgramFilesDir is the machine-wide install folder.
func ProgramFilesDir(app string) string {
	pf := os.Getenv("ProgramW6432")
	if pf == "" {
		pf = os.Getenv("ProgramFiles")
	}
	return filepath.Join(pf, app)
}

// ProgramDataDir is the machine-wide settings folder.
func ProgramDataDir(app string) string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, app)
}

// SecureDir creates dir readable only by SYSTEM and Administrators.
func SecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// SetRunAtSignInAllUsers adds or removes a visible startup entry for every
// user (HKLM Run). Unattended access uses it for the tray notice.
func SetRunAtSignInAllUsers(name, cmdline string, on bool) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(name, cmdline)
	}
	if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// AddUninstallEntryMachine lists the app in Settings > Apps for everyone.
func AddUninstallEntryMachine(key, displayName, version, exe, uninstall string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("DisplayName", displayName)
	_ = k.SetStringValue("DisplayVersion", version)
	_ = k.SetStringValue("Publisher", "Remote Support Manager (self-hosted)")
	_ = k.SetStringValue("DisplayIcon", exe)
	_ = k.SetStringValue("UninstallString", uninstall)
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	return nil
}

// RemoveUninstallEntryMachine deletes the machine-wide Settings > Apps entry.
func RemoveUninstallEntryMachine(key string) {
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+key)
}

const sasPolicy = `Software\Microsoft\Windows\CurrentVersion\Policies\System`

// EnableServiceSAS lets services send Ctrl+Alt+Del (policy value 1 or 3).
// It returns the previous value (-1 when unset) so uninstall can restore it.
func EnableServiceSAS() (int, error) {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, sasPolicy, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return -1, err
	}
	defer k.Close()
	prev := -1
	if v, _, err := k.GetIntegerValue("SoftwareSASGeneration"); err == nil {
		prev = int(v)
		if v == 1 || v == 3 {
			return prev, nil
		}
		if v == 2 { // ease of access apps only: add services
			return prev, k.SetDWordValue("SoftwareSASGeneration", 3)
		}
	}
	return prev, k.SetDWordValue("SoftwareSASGeneration", 1)
}

// RestoreServiceSAS puts back the value EnableServiceSAS found.
func RestoreServiceSAS(prev int) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, sasPolicy, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if prev < 0 {
		_ = k.DeleteValue("SoftwareSASGeneration")
	} else {
		_ = k.SetDWordValue("SoftwareSASGeneration", uint32(prev))
	}
}

// StartInConsoleSession starts cmdline as LocalSystem in the session of the
// user at the physical console (or the sign-in screen when nobody is signed
// in). It must be called from a LocalSystem service.
func StartInConsoleSession(exe, cmdline string) (*os.Process, error) {
	sid := windows.WTSGetActiveConsoleSessionId()
	if sid == 0xFFFFFFFF {
		return nil, errors.New("no console session")
	}
	var own windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_SESSIONID|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ASSIGN_PRIMARY, &own); err != nil {
		return nil, err
	}
	defer own.Close()
	var tok windows.Token
	if err := windows.DuplicateTokenEx(own, windows.MAXIMUM_ALLOWED, nil, windows.SecurityIdentification, windows.TokenPrimary, &tok); err != nil {
		return nil, err
	}
	defer tok.Close()
	if err := windows.SetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&sid)), 4); err != nil {
		return nil, fmt.Errorf("set session: %w", err)
	}
	si := &windows.StartupInfo{Desktop: W(`winsta0\default`)}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation
	cl, _ := windows.UTF16FromString(cmdline)
	if err := windows.CreateProcessAsUser(tok, W(exe), &cl[0], nil, nil, false, windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, nil, nil, si, &pi); err != nil {
		return nil, err
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return os.FindProcess(int(pi.ProcessId))
}

// DesktopWorker runs screen capture and input on one OS thread that
// follows the input desktop, so a technician with unattended access sees
// and can use the sign-in screen, the lock screen and UAC prompts. The
// thread owns no windows, which SetThreadDesktop requires.
type DesktopWorker struct {
	once sync.Once
	jobs chan func()
	cur  string
	h    uintptr
}

// Do runs fn on the worker thread after switching to the input desktop.
func (d *DesktopWorker) Do(fn func()) {
	d.once.Do(func() {
		d.jobs = make(chan func())
		go func() {
			runtime.LockOSThread()
			for j := range d.jobs {
				d.follow()
				j()
			}
		}()
	})
	done := make(chan struct{})
	d.jobs <- func() { fn(); close(done) }
	<-done
}

func (d *DesktopWorker) follow() {
	h := call(pOpenInputDesktop, 0, 0, 0x10000000 /* GENERIC_ALL */)
	if h == 0 {
		return
	}
	name := desktopName(h)
	if strings.EqualFold(name, d.cur) {
		call(pCloseDesktop, h)
		return
	}
	if call(pSetThreadDesktop, h) != 0 {
		if d.h != 0 {
			call(pCloseDesktop, d.h)
		}
		d.cur, d.h = name, h
		return
	}
	call(pCloseDesktop, h)
}

func desktopName(h uintptr) string {
	buf := make([]uint16, 256)
	var n uint32
	if call(pGetUserObjectInformation, h, 2 /* UOI_NAME */, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n))) == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
