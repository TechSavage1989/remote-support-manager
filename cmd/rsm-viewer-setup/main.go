// Command rsm-viewer-setup installs the Remote Support Manager viewer for
// the current Windows user (no administrator rights): it copies the
// embedded viewer to %LOCALAPPDATA%\Programs, adds a Start menu shortcut,
// registers rsm:// links and an entry in Settings > Apps. The same viewer
// also ships as a portable .exe that runs without installing.
package main

import (
	"embed"
	"io/fs"
)

// Version is set at build time.
var Version = "0.1.0-dev"

//go:embed payload
var payload embed.FS

const viewerExe = "RemoteSupportViewer.exe"

// viewerBytes returns the embedded viewer, or nil when the installer was
// built without one (a plain `go build` in a clean checkout).
func viewerBytes() []byte {
	b, err := fs.ReadFile(payload, "payload/"+viewerExe)
	if err != nil || len(b) < 2 || b[0] != 'M' || b[1] != 'Z' {
		return nil
	}
	return b
}
