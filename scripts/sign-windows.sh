#!/usr/bin/env bash
# Signs Windows executables in place with Azure Artifact Signing, the same
# account and certificate profile as the owner's other apps (see
# docs/CODE_SIGNING.md). Used by build-windows.sh when RSM_SIGN=1, on a
# Windows runner that already signed in to Azure (azure/login with OIDC).
#
#   scripts/sign-windows.sh file.exe [file.exe ...]
#
# Needs SIGN_ENDPOINT, SIGN_ACCOUNT, SIGN_PROFILE and SIGN_TIMESTAMP_URL, the
# `sign` .NET tool and PowerShell. Fails unless every file ends up with a
# Valid, timestamped Authenticode signature.
set -euo pipefail
: "${SIGN_ENDPOINT:?}" "${SIGN_ACCOUNT:?}" "${SIGN_PROFILE:?}" "${SIGN_TIMESTAMP_URL:?}"
winpath() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else printf '%s' "$1"; fi; }
files=()
for f in "$@"; do files+=("$(winpath "$f")"); done
sign code artifact-signing \
  --artifact-signing-endpoint "$SIGN_ENDPOINT" \
  --artifact-signing-account "$SIGN_ACCOUNT" \
  --artifact-signing-certificate-profile "$SIGN_PROFILE" \
  --azure-credential-type azure-cli \
  --timestamp-url "$SIGN_TIMESTAMP_URL" --timestamp-digest SHA256 --file-digest SHA256 \
  --description "Remote Support Manager" \
  --description-url "https://github.com/TechSavage1989/remote-support-manager" \
  "${files[@]}"
for f in "${files[@]}"; do
  RSM_FILE="$f" powershell.exe -NoProfile -NonInteractive -Command '
    $s = Get-AuthenticodeSignature -FilePath $env:RSM_FILE
    if ($s.Status -ne "Valid") { Write-Error "$($env:RSM_FILE) is not validly signed: $($s.Status) $($s.StatusMessage)"; exit 1 }
    if (-not $s.TimeStamperCertificate) { Write-Error "$($env:RSM_FILE) has no timestamp"; exit 1 }
    Write-Output "Signed: $($env:RSM_FILE) by $($s.SignerCertificate.Subject)"'
done
