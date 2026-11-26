// Command rsm-server-setup is RemoteSupportServerSetup.exe: the
// no-command-line way to install the Remote Support Manager server on a
// Windows computer. It asks once, unpacks the embedded server and client
// downloads, and hands over to "rsm-server install" through the Windows
// administrator prompt. That installs a Windows service, a firewall rule for
// the server program and a Start menu link to the local setup page, then
// opens the setup page in the browser.
package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Version is set at build time.
var Version = "0.1.0-dev"

//go:embed payload
var payload embed.FS

// hasServer reports whether the build scripts embedded rsm-server.exe.
func hasServer() bool {
	b, err := fs.ReadFile(payload, "payload/rsm-server.exe")
	return err == nil && len(b) > 2 && b[0] == 'M' && b[1] == 'Z'
}

// unpack writes the embedded files (except the README) under dir.
func unpack(dir string) error {
	return fs.WalkDir(payload, "payload", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, "payload"), "/")
		if rel == "" || rel == "README.md" {
			return nil
		}
		t := filepath.Join(dir, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		b, err := fs.ReadFile(payload, p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o755)
	})
}
