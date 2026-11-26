# Development handoff

Current state of Remote Support Manager for the next person or AI session. Read [AGENTS.md](AGENTS.md) first.

## Current version

- Version: **0.2.0-preview.1** (pre-release, published 2026-11-26 by the Release workflow from `eb7ca02`; zip checksum verified). 0.1.0-preview.1 was built but never published.
- Repository: https://github.com/TechSavage1989/remote-support-manager (public), default branch `main`.
- Update feed: each self-hosted server serves `/updates/<product>-manifest.json` (+ `.sig`); there is no central feed.

## Code map

See the folder table in [AGENTS.md](AGENTS.md#folder-responsibilities). Entry points: `cmd/rsm-server`, `cmd/rsm-agent`, `cmd/rsm-viewer`, `cmd/rsm-viewer-setup`, `cmd/rsm-release`.

Protocol: `[4-byte big-endian header length][JSON header with "t"][binary payload]`, header ≤ 1 MB, message ≤ 8 MB. Message type prefixes map to permissions: `scr.` view, `in.` control, `clip.` clipboard, `fs.`/`xf.` transfer. Session control (`session.*`, `ping`, `denied`) needs none.

Client types (`protocol.Kind*`, stored on the device and the invitation):

| Kind | Created by | Install | Consent | Removal |
|---|---|---|---|---|
| `onetime` | technician | none (runs from the download) | once, in the invitation window; 6-digit code or personal link | after its session ends, or 3 min disconnected (`Config.OneTimeGrace`) |
| `support` | technician | per user, HKCU Run | once; customer's choice of permissions saved as `devices.standing` | technician presses Close support session |
| `access` | administrator | machine-wide service + HKLM Run tray notice, UAC | Windows administrator, explicit tick | administrator removes the device |
| `attended` | administrator | per user | every session with a code | administrator removes the device |

Invitations live in `enroll_tokens` (token and code stored as hashes). `GET /api/invite` shows the invitation to the client; `POST /api/agent/enroll` takes `token` or `code` plus the permissions the customer allowed. Downloads: `/download/i/{token}` (zip with `client.json`), `/download/client` (generic one-time client, needs a code), `/download/viewer[?portable=1]`. Removal: `POST /api/devices/{id}/remove` → credential revoked + `device.remove` → client uninstalls (also on HTTP 401).

Standing kinds: `CreateSession` grants `requested ∩ technician ∩ device.standing`, marks the session active and sends `session.start{standing:true}`; no code prompt.

Session lifecycle (attended): technician creates a session (6-digit code + one-time launch ticket) → agent consent prompt → server marks it active and sends `session.start` → agent opens `/ws/agent/session?sid=` → `session.hello` (epoch 1). Reconnects within 60 s get `session.resumed` (epoch + 1). Ended sessions return HTTP 410 to the viewer.

## Build, test and package

```bash
go vet ./... && GOOS=windows go vet ./...
go test -race ./...
bash scripts/check-root.sh && bash scripts/test-check-root.sh
bash scripts/build-windows.sh 0.2.0-preview.1 [update-public-key]
```

Windows native: `powershell -File scripts\build-windows.ps1 -Version 0.2.0-preview.1`. Both scripts put the client and viewer files in `downloads\` and build the viewer installer by copying the viewer into `cmd/rsm-viewer-setup/payload/` and removing it afterwards. Requires Go 1.26 (the `go` line in `go.mod`; newer toolchains download automatically).

Release (`.github/workflows/release.yml`): runs when `docs/releases/` changes on `main` and publishes the newest version there if it is not released yet (the owner's other repositories work the same way); also on a pushed `v*` tag or manually with a tag. See AGENTS.md → Workflow rules. Executables are not Authenticode-signed yet; Azure signing like the owner's other repos can be added once its secrets exist in this repository.

CI (`.github/workflows/ci.yml`): Linux job (root guard + negative control, gofmt, vet for Linux and Windows, race tests, Windows cross-build) and Windows job (tests, native build, executables start, downloads present, installer embeds the viewer).

## Settings and data paths

| Component | Path |
|---|---|
| Viewer settings | `%LOCALAPPDATA%\RemoteSupportManager\viewer.json` |
| Installed viewer | `%LOCALAPPDATA%\Programs\Remote Support Manager Viewer\RemoteSupportViewer.exe` (Start menu shortcut, HKCU Uninstall `RemoteSupportManagerViewer`) |
| Support / attended client | `%LOCALAPPDATA%\Programs\Remote Support Manager\RemoteSupportAgent.exe` (HKCU Run `RemoteSupportManagerAgent`, Apps entry) |
| Support / attended settings, credential (user DPAPI), log | `%LOCALAPPDATA%\RemoteSupportManager\agent.json`, `agent.log` |
| One-time client | nothing on disk; credential in memory |
| Unattended access | `%ProgramFiles%\Remote Support Manager Access\` (exe, `access.json`), service `RemoteSupportManagerAccess`, HKLM Run `RemoteSupportManagerAccessNotice`, HKLM Uninstall entry; `%ProgramData%\Remote Support Manager Access\agent.json` (machine DPAPI, SYSTEM/Administrators ACL) and `agent.log`; sets `SoftwareSASGeneration` and restores it on uninstall |
| Update downloads | `%LOCALAPPDATA%\RemoteSupportManager\updates\<product>\` |
| Rollback copy | `<exe>.previous`, pending record `<exe>.update.json` |
| Server | `--data` folder: `rsm.db` (SQLite), `server.crt`, `server.key` |

## Validation

| Check | Result |
|---|---|
| Unit and end-to-end tests, Linux, race detector | Pass |
| `GOOS=windows go vet` and cross-build | Pass |
| Root guard and negative control | Pass |
| Branch cleanup rules, dry run against the live repo | Pass: 0 to delete, 1 kept (`feat/first-working-build`, unique commits) |
| Tests and build on GitHub's Windows runner (windows-latest) | Pass: all packages including the end-to-end test; native build; server and release tool start |
| Session types: one-time with code, support with standing consent and removal, unattended admin rules, download zip contents (`TestClientKinds`) | Pass (Linux, race) |
| Console: create one-time and support invitations, download zip and viewer files, standing connect, Close support session uninstalls a simulated client (headless Chromium against a local server) | Pass |
| Native windows on the owner's PC (viewer, agent consent, bar, file manager, clipboard, keyboard) | Pending: Go is not installed on the owner's PC; waiting for their choice |
| Invitation window, per-user support install and self-uninstall, unattended service, session helper, lock-screen capture, SendSAS, viewer installer on real Windows | Unverified: built and vetted for Windows only. Steps in docs/TESTING.md section 4 |
| Two-machine test (technician PC + Windows endpoint VM) | Unverified: no Windows VM available yet; exact steps in [docs/TESTING.md](docs/TESTING.md) |
| Previous-version → new-version update on Windows | Unverified; steps in docs/TESTING.md section 6 |

## Known limitations

- Ctrl+Alt+Del and the sign-in/UAC desktops work only with unattended access (LocalSystem helper).
- The unattended service has no update flow yet; install a newer download over it.
- Unattended sessions run as LocalSystem: files received go where the technician chooses and are owned by SYSTEM; the file manager starts at `C:\Users` and the drives.
- Uninstalling a support client from Settings → Apps leaves the device listed (offline) in the console until someone closes it there.
- Standing sessions show as open in the console as soon as they are created, before the technician opens the viewer.
- GDI capture; DXGI Desktop Duplication would be faster on large screens.
- The update notice is drawn natively with GDI. It has no animation (spinning icon or moving stripes); that also satisfies the reduced-motion rule. Light/dark follows the Windows app theme; contrast checked by colour choice, not by an automated test.
- Release history link points at the GitHub releases page.
- Preview executables are not Authenticode-signed. Azure Trusted Signing is set up by the owner; signing must happen before `rsm-release manifest`.
- No screenshots in the README yet: none have been captured from a real session.

## Decisions and approvals (history; the standing merge-and-release instruction is in AGENTS.md)

Task: build the first working version (2026-11-26). Same task, later the same day: the owner asked for a portable client and an installer, an unattended support format, and a support format that stays connected as long as needed and uninstalls itself when the session is deleted; the coordinator added that both must stay customer-authorised and visible. The owner approved for this task: creating and publishing the repository as public, pushing, opening the PR and marking it ready, merging after checks, and publishing a prerelease; Remote Control tests on their PC or server; the solution must be free apart from code signing, which they already have in Azure. On 2026-11-26 the owner wrote: "I approve publishing Remote Support Manager v0.2.0-preview.1 as a pre-release to TechSavage1989/remote-support-manager, including the automatic release workflow." That approval covers this one release only. Later the same day the owner gave standing permission: "add standing permission to merge and release to AGENTS.md like my other apps, merge it when checks pass". It is recorded in AGENTS.md → Workflow rules, so later releases need no separate approval.

## Open items

- License: the repository has no license file, so the default is "all rights reserved". The owner decides.
- Windows endpoint VM for two-machine testing.
- Update signing key: created and kept offline by the owner; the public key is passed to the build. No key exists in the repository.

## Branches

No other branches with unique work.
