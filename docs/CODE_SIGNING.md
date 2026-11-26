# Code signing (Azure Artifact Signing)

The Release workflow signs every Windows executable with Azure Artifact
Signing, using the same account, certificate profile and GitHub OIDC approach
as the owner's other apps (Network Toolkit, Server Inventory Configuration
Manager). Signing is required: a release fails unless the three repository
secrets below exist and every signature is Valid and timestamped. It never
falls back to unsigned (owner's request, 2026-11-26).

Configuration in `.github/workflows/release.yml` (not secret):

- Endpoint: `https://eus.codesigning.azure.net` (East US)
- Account: `networktoolkit-signing`
- Certificate profile: `jpaull-codesign`
- Signing client: Microsoft's `sign` .NET tool, pinned in `SIGN_TOOL_VERSION`
- Timestamp: `http://timestamp.acs.microsoft.com`, SHA-256 file and timestamp digests

## How it works

1. The `check` job (Linux) picks the version and runs every check and test.
2. The `release` job runs on Windows in the GitHub `release` environment with
   `id-token: write`, so its OIDC token subject is
   `repo:TechSavage1989/remote-support-manager:environment:release`. It signs
   in with `azure/login` (no client secret or password) and runs
   `scripts/build-windows.sh` with `RSM_SIGN=1`.
3. The build signs each program right after it is built, through
   `scripts/sign-windows.sh`, which also checks each signature with
   `Get-AuthenticodeSignature`. The order matters: the viewer is signed before
   `RemoteSupportViewerSetup.exe` embeds it, and `rsm-server.exe`, the agent
   and the viewer files are signed before `RemoteSupportServerSetup.exe` embeds
   them. The two setups are signed last.
4. `SHA256SUMS.txt`, the zip and its `.sha256` are made from the signed files.
   The workflow unpacks the zip, checks every `.exe` signature again, publishes
   and verifies the uploaded hashes. It signs out of Azure at the end.

- Never sign or re-sign a published asset; signing changes the bytes and the
  published SHA-256. A signed build of an already published version gets a new
  version number.
- Update manifests (`rsm-release manifest`) must be made from the signed files.
- Local builds (`build-windows.sh` without `RSM_SIGN`, `build-windows.ps1`) and
  CI builds are unsigned.

## Setup the owner does (once)

The Artifact Signing account, identity validation, certificate profile, app
registration and its signer role already exist from the Network Toolkit setup.
This repository needs only:

1. **A federated credential for this repository** on the same Entra app
   registration (Azure portal → Microsoft Entra ID → App registrations → the
   signing app → Certificates & secrets → Federated credentials → Add
   credential → scenario *GitHub Actions deploying Azure resources*):
   Organization `TechSavage1989`, Repository `remote-support-manager`, Entity
   type **Environment**, environment name `release`. Use an Environment
   credential, not a branch or tag one: Entra matches the subject literally.
2. **Repository secrets** (GitHub → this repository → Settings → Secrets and
   variables → Actions → New repository secret), the same values as Network
   Toolkit's: `AZURE_CLIENT_ID` (Application ID), `AZURE_TENANT_ID`
   (Directory ID) and `AZURE_SUBSCRIPTION_ID`. None of them is a password.

The `release` environment is created by the first run that uses it; do not add
required reviewers unless every release should wait for a manual approval.
