# Builds the Windows x64 executables natively on Windows.
#   powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 [-Version 0.1.0-dev] [-UpdatePublicKey <key>]
# Requires Go 1.24 or later. Output goes to dist\. The executables are not
# Authenticode-signed by this script.
param([string]$Version = "0.1.0-dev", [string]$UpdatePublicKey = "")
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
$mod = "github.com/TechSavage1989/remote-support-manager"
$ld = "-s -w -X main.Version=$Version -X $mod/internal/agentcore.Version=$Version -X $mod/internal/server.Version=$Version"
if ($UpdatePublicKey) { $ld += " -X $mod/internal/update.PublicKey=$UpdatePublicKey" }
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "$ld -H windowsgui" -o dist\RemoteSupportViewer.exe .\cmd\rsm-viewer; if ($LASTEXITCODE) { exit 1 }
go build -trimpath -ldflags "$ld -H windowsgui" -o dist\RemoteSupportAgent.exe .\cmd\rsm-agent; if ($LASTEXITCODE) { exit 1 }
go build -trimpath -ldflags "$ld" -o dist\rsm-server.exe .\cmd\rsm-server; if ($LASTEXITCODE) { exit 1 }
go build -trimpath -ldflags "-s -w" -o dist\rsm-release.exe .\cmd\rsm-release; if ($LASTEXITCODE) { exit 1 }
Get-FileHash dist\*.exe -Algorithm SHA256 | Format-Table -AutoSize
