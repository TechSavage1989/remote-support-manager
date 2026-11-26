//go:build windows

package winapi

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Disk is one fixed drive.
type Disk struct {
	Name        string
	Total, Free uint64
}

// FixedDisks lists local fixed drives with their space.
func FixedDisks() []Disk {
	buf := make([]uint16, 256)
	n := call(pGetDrives, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	var out []Disk
	// The buffer is a list of NUL-terminated strings.
	start := 0
	for i := 0; i < int(n); i++ {
		if buf[i] != 0 {
			continue
		}
		root := windows.UTF16ToString(buf[start:i])
		start = i + 1
		if root == "" {
			continue
		}
		const driveFixed = 3
		if call(pGetDriveType, uintptr(unsafe.Pointer(W(root)))) != driveFixed {
			continue
		}
		var free, total, totalFree uint64
		if call(pGetDiskFree, uintptr(unsafe.Pointer(W(root))), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree))) != 0 {
			out = append(out, Disk{Name: strings.TrimSuffix(root, `\`), Total: total, Free: free})
		}
	}
	return out
}

// UptimeSeconds since boot.
func UptimeSeconds() int64 { return int64(call(pGetTick64) / 1000) }

// OSVersion returns a friendly name and the version/build string.
func OSVersion() (name, version string) {
	v := windows.RtlGetVersion()
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	product, display := "Windows", ""
	var ubr uint64
	if err == nil {
		defer k.Close()
		if s, _, err := k.GetStringValue("ProductName"); err == nil {
			product = s
		}
		display, _, _ = k.GetStringValue("DisplayVersion")
		ubr, _, _ = k.GetIntegerValue("UBR")
	}
	// ProductName still says "Windows 10" on Windows 11.
	if v.BuildNumber >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	if display != "" {
		product += " " + display
	}
	return product, fmt.Sprintf("%d.%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber, ubr)
}

// CurrentUser returns DOMAIN\user.
func CurrentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// KnownFolder returns a shell folder such as Desktop or Downloads.
func KnownFolder(id *windows.KNOWNFOLDERID) string {
	p, err := windows.KnownFolderPath(id, 0)
	if err != nil {
		return ""
	}
	return p
}

// MarkOfTheWeb tags a received file as coming from another computer, so
// Windows SmartScreen and Office Protected View treat it as downloaded.
func MarkOfTheWeb(path string) {
	_ = os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}

// Protect encrypts data for the current Windows user (DPAPI).
func Protect(b []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, W("Remote Support Manager"), nil, 0, nil, 0x1 /* UI_FORBIDDEN */, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// Unprotect decrypts DPAPI data.
func Unprotect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty credential")
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0x1, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// RegisterURLProtocol registers rsm:// for the current user only, pointing at
// exe. It never touches machine-wide settings.
func RegisterURLProtocol(scheme, exe, description string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+scheme, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("", "URL:"+description)
	_ = k.SetStringValue("URL Protocol", "")
	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+scheme+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmd.Close()
	return cmd.SetStringValue("", `"`+exe+`" "%1"`)
}

// UnregisterURLProtocol removes the per-user handler.
func UnregisterURLProtocol(scheme string) {
	for _, p := range []string{`\shell\open\command`, `\shell\open`, `\shell`, ``} {
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+scheme+p)
	}
}

// SetRunAtSignIn adds or removes a visible per-user startup entry.
func SetRunAtSignIn(name, cmdline string, on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(name, cmdline)
	}
	err = k.DeleteValue(name)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

// ProgramsDir is the per-user install folder (no administrator rights).
func ProgramsDir(app string) string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", app)
}

// AddUninstallEntry lists the app in Settings > Apps for the current user.
func AddUninstallEntry(key, displayName, version, exe, uninstall string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+key, registry.SET_VALUE)
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

// RemoveUninstallEntry deletes the Settings > Apps entry.
func RemoveUninstallEntry(key string) {
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+key)
}

func readDWORD(path, name string) (uint32, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return uint32(v), err
}
