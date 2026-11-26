#!/usr/bin/env bash
# Builds the Windows x64 test package from Linux, macOS or Git Bash.
#
#   scripts/build-windows.sh [version] [update-public-key]
#
# Output: dist/RemoteSupportManager-<version>-windows-x64.zip plus SHA256SUMS.
# The executables are NOT Authenticode-signed by this script; sign them
# (for example with Azure Trusted Signing) before creating update manifests.
set -euo pipefail
cd "$(dirname "$0")/.."
ver="${1:-0.1.0-dev}"
pub="${2:-}"
mod=github.com/TechSavage1989/remote-support-manager
ld="-s -w -X main.Version=$ver -X $mod/internal/agentcore.Version=$ver -X $mod/internal/server.Version=$ver"
[ -n "$pub" ] && ld="$ld -X $mod/internal/update.PublicKey=$pub"
out="dist/RemoteSupportManager-$ver-windows-x64"
rm -rf "$out" "$out.zip"
mkdir -p "$out"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
mkdir -p "$out/downloads"
go build -trimpath -ldflags "$ld -H windowsgui" -o "$out/downloads/RemoteSupportViewer.exe" ./cmd/rsm-viewer
# The installer embeds the viewer; the copy in payload/ is build output.
payload=cmd/rsm-viewer-setup/payload/RemoteSupportViewer.exe
cp "$out/downloads/RemoteSupportViewer.exe" "$payload"
sp=cmd/rsm-server-setup/payload
trap 'rm -f "$payload" "$sp/rsm-server.exe"; rm -rf "$sp/downloads"' EXIT
go build -trimpath -ldflags "$ld -H windowsgui" -o "$out/downloads/RemoteSupportViewerSetup.exe" ./cmd/rsm-viewer-setup
rm -f "$payload"
go build -trimpath -ldflags "$ld -H windowsgui" -o "$out/downloads/RemoteSupportAgent.exe" ./cmd/rsm-agent
go build -trimpath -ldflags "$ld" -o "$out/rsm-server.exe" ./cmd/rsm-server
# The server setup embeds rsm-server.exe and the downloads folder (build output in payload/).
cp "$out/rsm-server.exe" "$sp/"
cp -r "$out/downloads" "$sp/downloads"
go build -trimpath -ldflags "$ld -H windowsgui" -o "$out/RemoteSupportServerSetup.exe" ./cmd/rsm-server-setup
rm -rf "$sp/rsm-server.exe" "$sp/downloads"
go build -trimpath -ldflags "-s -w" -o "$out/rsm-release.exe" ./cmd/rsm-release
cp docs/TESTING.md "$out/TESTING.md"
cat > "$out/README-FIRST.txt" <<TXT
Remote Support Manager $ver - Windows x64 test package
UNSIGNED TEST BUILD. Windows SmartScreen may warn before running it.

  RemoteSupportServerSetup.exe            START HERE: installs the server on this Windows computer (no command line).
                                          A setup page then opens in your browser.
  rsm-server.exe                          the server itself, for people who prefer the command line
  rsm-release.exe                         update signing tool for administrators
  downloads\RemoteSupportViewerSetup.exe  technician viewer installer (per user, no admin rights)
  downloads\RemoteSupportViewer.exe       portable technician viewer (runs without installing)
  downloads\RemoteSupportAgent.exe        endpoint client; the console wraps it with client.json
                                          for one-time, support, unattended or attended use

rsm-server.exe serves the downloads folder next to it, so technicians can get the
viewer and customers can get their client from the console's links.

Follow TESTING.md for the step-by-step local test. Use test VMs and made-up data only.
Uninstall from Settings > Apps (Remote Support Manager agent / Viewer / unattended access).
TXT
(cd "$out" && sha256sum *.exe downloads/*.exe > SHA256SUMS.txt)
(cd dist && rm -f "$(basename "$out").zip" && if command -v zip >/dev/null; then zip -qr "$(basename "$out").zip" "$(basename "$out")"; else python3 -m zipfile -c "$(basename "$out").zip" "$(basename "$out")"; fi)
(cd dist && sha256sum "$(basename "$out").zip" > "$(basename "$out").zip.sha256")
echo "Built dist/$(basename "$out").zip"
cat "$out/SHA256SUMS.txt"
