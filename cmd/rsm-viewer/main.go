// Command rsm-viewer is the native Windows technician viewer for Remote
// Support Manager. The console's "Open in viewer" link (rsm://connect?...)
// starts it; the link carries a one-time ticket, never a password.
//
// A native window is used instead of a browser so keyboard shortcuts such as
// Alt+Tab, the Windows key and Ctrl+Esc can be sent to the remote PC, and so
// the clipboard can carry images and rich text both ways.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Version is set at build time.
var Version = "0.1.0-dev"

type settings struct {
	AskedProtocol bool   `json:"askedProtocol"`
	UpdateFeed    string `json:"updateFeed"`
	AutoCheck     *bool  `json:"autoCheck,omitempty"`
	LastCheck     int64  `json:"lastCheck"`
	SkipVersion   string `json:"skipVersion"`
	// LastServer and LastPin remember the server of the last session so
	// update checks can use it (pinned TLS, same as sessions).
	LastServer string `json:"lastServer"`
	LastPin    string `json:"lastPin"`
}

func settingsPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		dir, _ = os.UserConfigDir()
	}
	return filepath.Join(dir, "RemoteSupportManager", "viewer.json")
}

func loadSettings() *settings {
	s := &settings{}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, s)
	}
	return s
}

func (s *settings) save() {
	p := settingsPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(p+".tmp", b, 0o600)
	_ = os.Rename(p+".tmp", p)
}

func main() { run() }
