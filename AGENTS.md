# AGENTS.md

Read-first guide for everyone working on Remote Support Manager: people and every AI tool (Claude, ChatGPT/Codex, Copilot, Cursor, Gemini and others). Then read [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md) and the shared standard [docs/REPOSITORY_STANDARDS.md](docs/REPOSITORY_STANDARDS.md). Where this file and the standard differ, this file wins; enforced check scripts win over both.

## What this is

A self-hostable, Windows-first remote support tool: a Go server with a web console, a Windows client on endpoints (one-time portable, per-user support session, machine-wide unattended access, or attended agent), and a native Windows viewer for technicians (installer or portable). ScreenConnect's public docs were used only as a feature reference; never copy code, text, assets or branding from any product.

## Hard rules (product)

- Customer-authorised only. Every client type starts with the endpoint user (or, for unattended access, a Windows administrator through UAC) seeing who is asking and what for, and agreeing. Attended agents ask again with a code for every session; one-time and support clients record that consent once (`devices.standing`) and the server caps every session to it. Unattended access is opt-in per device, created only by console administrators.
- Removal uninstalls: closing a one-time or support session, or removing a device, sends `device.remove`; the client uninstalls everything it added. Keep that path working for every client type.
- Always visible: tray icon while the agent runs, amber bar while a session runs. Never add stealth installs or ways to hide the indicator.
- No inbound ports on endpoints, no public RDP, no endpoint-protection bypasses, no remote command shells, scripting or fleet-wide commands.
- The server enforces client isolation and the four permissions (view, control, clipboard, transfer) on every API call and relayed message. Never move a check into the viewer only.
- Audit metadata only. Never log screen images, keystrokes, clipboard contents or file contents.
- Received files are never opened or run automatically.
- Tests and screenshots use fictional data only (`example.test`, `192.0.2.x`, `Contoso Test`).
- Internet access (owner's request, 2026-11-26): the server may be reached from the internet through Tailscale Funnel, the owner's own name with router port 443 and a Let's Encrypt certificate, or a Cloudflare Tunnel, all chosen by the owner on the local setup page. Everything must stay free. Endpoints still never listen. AI sessions build and document this; they do not provision servers, change routers or DNS, or deploy to real customers.
- No command line for the owner: installing and configuring the server goes through `RemoteSupportServerSetup.exe` and the loopback-only setup page (`http://127.0.0.1:8080`). The setup page refuses non-loopback clients, foreign Host headers and cross-site posts, and needs an administrator's password once the first account exists.

## Folder responsibilities

| Path | Responsibility |
|---|---|
| `cmd/rsm-server` | Server: `run` (no-command-line mode: setup page, settings.json, Let's Encrypt or tunnel, restarts on change; what the Windows service runs), Windows `service`/`install`/`uninstall`, Tailscale Funnel helper, and the CLI `init`, `serve`, `fingerprint`, `reset-mfa`, `version` |
| `cmd/rsm-server-setup` | `RemoteSupportServerSetup.exe`: embeds `rsm-server.exe` and `downloads/` (build output copied into `payload/`), asks once, then runs `rsm-server install` elevated |
| `cmd/rsm-agent` | Windows client: invitation window, one-time (portable), support (per user), unattended access (service `access_windows.go`, session helper, tray notice), consent window, session bar, tray, updates, self-uninstall. `*_other.go` is a simulator for non-Windows tests |
| `cmd/rsm-viewer-setup` | Per-user viewer installer; embeds the viewer that the build scripts copy into `payload/` (build output, never committed) |
| `cmd/rsm-viewer` | Native Windows viewer: screen, keyboard capture, menus, file manager, clipboard, update notice |
| `cmd/rsm-release` | Update signing keys and signed manifests |
| `internal/protocol` | Wire format and message types; permission class by type prefix |
| `internal/server` | HTTP API, web console (`web/`, embedded), SQLite store, auth, invitations and client downloads (`invites.go`, `clients.go`), session hub and relay, setup page and settings (`setup.go`, `settings.go`) |
| `internal/agentcore`, `internal/viewercore` | Platform-neutral agent and viewer logic (tested on Linux) |
| `internal/transfer`, `internal/clipsync`, `internal/keys`, `internal/screen`, `internal/pathsafe`, `internal/tlspin` | Engines shared by agent and viewer |
| `internal/update` | Manifest verification, download, staging, install, rollback (platform-neutral, tested) |
| `internal/updateui` | Windows update UI shared by viewer and agent |
| `internal/winapi` | Thin Win32 bindings (windows build tag only) |
| `tests/e2e` | End-to-end test with a real TLS server and simulated agents |
| `scripts` | Root guard, its negative-control test, Windows build scripts, `sign-windows.sh` (Azure Artifact Signing, Release workflow only) |
| `docs` | Feature matrix, component evaluation, security model, testing steps, release notes (`docs/releases/`), images (`docs/images/`) |

## Root files

Allowed tracked root entries are listed in `scripts/root-allowlist.txt` and checked in CI by `scripts/check-root.sh` (git mode with `.git`, filesystem mode in an extracted archive, skipping only `dist`, `build`, `rsm-data`, `.git`, `.vscode`, `.idea`). `scripts/test-check-root.sh` is the negative control. To add a root file: add it to the allowlist in the same pull request and record the reason here. Current exceptions beyond the standard set: `go.mod`, `go.sum` (Go toolchain), `.gitattributes` (line endings, if added).

## Commands

```bash
go vet ./... && GOOS=windows go vet ./...      # windows vet: ignore "possible misuse of unsafe.Pointer"
go test -race ./...
bash scripts/check-root.sh && bash scripts/test-check-root.sh
bash scripts/build-windows.sh 0.2.0-dev [update-public-key]   # cross-build the Windows package into dist/ (downloads/ holds the client and viewer files the server serves)
```

On Windows: `powershell -File scripts\build-windows.ps1 -Version 0.2.0-dev`.

## Workflow rules

- Never commit to `main`. Branches: `feat/`, `fix/`, `docs/`, `chore/`, `release/`. One focused change per PR; merge only with green checks.
- `.github/workflows/delete-merged-branches.yml` deletes a branch after merge when its PR was merged at the branch's current commit, or when `main` contains all its commits and it is over a day old. It never touches `main`, protected branches or branches with open PRs, and lists kept branches with unique commits in the run summary. Manual runs default to a dry run.
- Before any push, inspect the diff for secrets, private data and licensing. Never commit build output, keys, databases or logs.
- Releases follow the owner's other app repositories: GitHub Actions publishes, not a person by hand. `.github/workflows/release.yml` runs when `docs/releases/` changes on `main`. If the newest `docs/releases/<version>.md` has no release yet, it re-runs the checks, builds the package, publishes the release (tag `v<version>` on that commit, pre-release when the version has a `-suffix`) with those notes, and verifies the uploaded hashes. So **merging new or changed release notes to `main` is the release step**; the owner's standing instruction below covers it. It refuses versions not newer than the latest release and never replaces one; when the newest version is already out it does nothing. A pushed `v*` tag or a manual run with an existing tag also works.
- Signing: releases are signed with Azure Artifact Signing in the Release workflow's Windows job (`release` environment, OIDC, secrets `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`); see docs/CODE_SIGNING.md. Sign each program before an installer embeds it. Never publish unsigned and never re-sign a published asset. AI sessions never create Azure credentials or GitHub secrets; the owner adds them.
- Versions: `main.Version` (viewer), `agentcore.Version`, `server.Version`, set together by the build scripts. Documentation-only changes do not bump versions or publish anything. Never overwrite a published release or update file (`rsm-release` refuses).
- Owner's standing instruction (2026-11-26): permission to merge and release is always granted, as in the owner's other app repositories. Push work to a short-lived branch, open the pull request, merge it once every required check is green, let the Release workflow publish when new release notes reach `main`, then verify the published assets. Don't ask first, and never ask the owner to create tags or releases by hand. This doesn't cover replacing an existing release or its assets, rewriting history, changing repository visibility or access, or merging with a failed or pending check.
- Other approvals are per task; record them in DEVELOPMENT_HANDOFF.md as history.

## Updates

- Checks: about 4 s after start, every 5 minutes while running, and on focus return when 2+ minutes have passed. Single-flight, ETag, exponential backoff after failures. A failed check shows "Update needs attention", never "up to date".
- Nothing downloads or restarts automatically. Buttons: **Download update**, then **Review and restart** → **Install and restart**. Install is refused while a session is active.
- Signed manifest (`<product>-manifest.json` + `.sig`, Ed25519) served by the self-hosted server at `/updates/`. The public key is built in with `-X .../internal/update.PublicKey=<key>`; without it, updates are off.
- Install keeps one backup (`<exe>.previous`), records `<exe>.update.json`, and rolls back after two unconfirmed starts.
- **Reuse these functions; do not build a second update UI** (`internal/updateui`):
  - `RenderUpdateCard`: the card (notice and settings)
  - `RenderWhatsNew`: the "What's new" card
  - `RenderDownloadBar`: the download bar and verification note
  - `ReleaseNoteLines` / `Highlights`: safe release-note rendering
  - `ShowReviewDialog`: install review; `ShowSettings`: the "App updates" section; `Card`: the notice window
- Platform note: the template's browser-specific items (CSS stripes, `<progress>`, prefers-reduced-motion, browser tests) are implemented natively: a GDI bar without animation, real buttons for keyboard access, and Go tests for state and note rendering.

## Messages to the owner

Two markers only, at the top of a message, and nowhere else:

- `**⚠️ ACTION NEEDED: ...**` when the owner's input, approval, credentials or a check on their own machine is needed. Say what to do, where, what to report back and what is blocked. Repeat unanswered requests in the next update.
- `**🚀 PUBLISHED: Remote Support Manager <version> is live**` only for a live release whose assets were verified.

Routine progress stays in plain short text. End each message with a short status: done, still running, and what (if anything) the owner must do.

## Gotchas

- `CW_USEDEFAULT` must be passed as a signed `int32` (`-0x80000000`), not converted from the untyped constant.
- Win32 control ids 1 and 2 are `IDOK` and `IDCANCEL`; dialog buttons use other ids.
- Low-level keyboard hooks swallow keys before `GetKeyState` sees them; track modifiers from the hook events.
- The viewer must not mark a session active until `session.hello` arrives from the agent.
- Standing sessions (one-time, support, unattended) are created active by the server; attended agents ignore a `session.start` they did not consent to. Only administrators create or remove unattended access and attended agents (`handleCreateInvite` and `handleRemoveDevice` check `IsAdmin`); keep it that way.
- The unattended service runs in session 0 with no windows; sessions run in a LocalSystem helper started in the console session (`winapi.StartInConsoleSession`). Capture and input go through `winapi.DesktopWorker`, one locked OS thread that follows the input desktop and owns no windows.
- The viewer installer and the agent share `%LOCALAPPDATA%\RemoteSupportManager`; uninstalling one must only delete its own files there.
- In tunnel mode the server listens on `127.0.0.1:8443` with its self-signed certificate; the tunnel holds the public certificate, so `CertPin` is empty and clients use the system roots. `Config.BehindTunnel` takes the client address from `X-Forwarded-For` only for loopback peers.
- Let's Encrypt (router mode) uses the TLS-ALPN challenge on port 443, so only 443 must be forwarded; port 80 is optional. Certificates are cached in `<data>/certificates`.
- The setup page and `rsm-server` CLI share the data folder; the Windows service uses `%ProgramData%\Remote Support Manager Server` (SYSTEM and administrators only).
- The relay only forwards message types allowed in each direction (`viewerMay` / `agentMay` in `internal/server/hub.go`); a new message type must be added there.
