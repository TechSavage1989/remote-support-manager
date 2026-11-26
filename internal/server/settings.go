package server

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Settings are the choices made on the local setup page. They live in
// <data>/settings.json next to the database so the server can run as a
// Windows service with no command-line flags.
type Settings struct {
	// OrgName is shown to customers in the invitation and consent windows.
	OrgName string `json:"orgName"`
	// PublicHost is the internet name customers and technicians use, for
	// example support.example.test. When set, the server gets a free
	// certificate for it from Let's Encrypt and listens on port 443.
	// When empty, the server uses its own self-signed certificate on port
	// 8443 (local network only).
	PublicHost string `json:"publicHost,omitempty"`
	// CertEmail is optional; Let's Encrypt uses it for expiry warnings.
	CertEmail string `json:"certEmail,omitempty"`
	// Tunnel names the program on this computer that carries internet
	// traffic for PublicHost ("tailscale" or "cloudflare"). The tunnel holds
	// the public certificate, so the server listens on 127.0.0.1 only and
	// does not ask Let's Encrypt itself. Empty means PublicHost points
	// straight at this computer (router port 443).
	Tunnel string `json:"tunnel,omitempty"`
}

// SettingsPath returns <data>/settings.json.
func SettingsPath(dataDir string) string { return filepath.Join(dataDir, "settings.json") }

// LoadSettings reads settings.json; a missing file gives defaults.
func LoadSettings(dataDir string) (Settings, error) {
	var st Settings
	b, err := os.ReadFile(SettingsPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return Settings{OrgName: "Remote Support"}, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	if st.OrgName == "" {
		st.OrgName = "Remote Support"
	}
	return st, nil
}

// SaveSettings writes settings.json atomically.
func SaveSettings(dataDir string, st Settings) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := SettingsPath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, SettingsPath(dataDir))
}

var hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// NormalizeHost accepts what a person might paste ("https://Support.Example.test/")
// and returns a bare lower-case DNS name, or an error explaining what is wrong.
// IP addresses are refused because free certificates need a name.
func NormalizeHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimRight(h, "/")
	if h == "" {
		return "", nil
	}
	if strings.ContainsAny(h, "/:@ ") {
		return "", errors.New("enter just the name, for example support.example.com")
	}
	if net.ParseIP(h) != nil {
		return "", errors.New("use a name such as yourname.duckdns.org, not a number address")
	}
	if !hostRE.MatchString(h) || len(h) > 253 {
		return "", errors.New("that doesn't look like an internet name, for example support.example.com")
	}
	return h, nil
}

// ValidEmail is a light check for the optional certificate email.
func ValidEmail(e string) bool {
	e = strings.TrimSpace(e)
	at := strings.LastIndex(e, "@")
	return at > 0 && at < len(e)-3 && strings.Contains(e[at:], ".") && !strings.ContainsAny(e, " <>\"")
}
