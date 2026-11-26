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
go build -trimpath -ldflags "$ld -H windowsgui" -o "$out/RemoteSupportViewer.exe" ./cmd/rsm-viewer
go build -trimpath -ldflags "$ld -H windowsgui" -o "$out/RemoteSupportAgent.exe" ./cmd/rsm-agent
go build -trimpath -ldflags "$ld" -o "$out/rsm-server.exe" ./cmd/rsm-server
go build -trimpath -ldflags "-s -w" -o "$out/rsm-release.exe" ./cmd/rsm-release
cp docs/TESTING.md "$out/TESTING.md"
cat > "$out/README-FIRST.txt" <<TXT
Remote Support Manager $ver - Windows x64 test package
UNSIGNED TEST BUILD. Windows SmartScreen may warn before running it.

  rsm-server.exe           self-hosted server and web console (runs on 127.0.0.1 by default)
  RemoteSupportViewer.exe  technician viewer (opened from the console's "Open in viewer")
  RemoteSupportAgent.exe   endpoint agent (enroll it on a test VM with the command from the console)
  rsm-release.exe          update signing tool for administrators

Follow TESTING.md for the step-by-step local test. Use test VMs and made-up data only.
Uninstall the agent from Settings > Apps > Remote Support Manager agent, or run:
  "%LOCALAPPDATA%\Programs\Remote Support Manager\RemoteSupportAgent.exe" uninstall
TXT
(cd "$out" && sha256sum *.exe > SHA256SUMS.txt)
(cd dist && rm -f "$(basename "$out").zip" && if command -v zip >/dev/null; then zip -qr "$(basename "$out").zip" "$(basename "$out")"; else python3 -m zipfile -c "$(basename "$out").zip" "$(basename "$out")"; fi)
(cd dist && sha256sum "$(basename "$out").zip" > "$(basename "$out").zip.sha256")
echo "Built dist/$(basename "$out").zip"
cat "$out/SHA256SUMS.txt"
