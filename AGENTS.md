# AGENTS.md

Read-first guide for everyone working on Remote Support Manager: people and every AI tool (Claude, ChatGPT/Codex, Copilot, Cursor, Gemini and others). Then read [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md) and the shared standard [docs/REPOSITORY_STANDARDS.md](docs/REPOSITORY_STANDARDS.md). Where this file and the standard differ, this file wins; enforced check scripts win over both.

## What this is

A self-hostable, Windows-first **attended** remote support tool: a Go server with a web console, a per-user Windows agent on endpoints, and a native Windows viewer for technicians. ScreenConnect's public docs were used only as a feature reference; never copy code, text, assets or branding from any product.

## Hard rules (product)

- Attended only. Every session needs the endpoint user to accept and type the technician's code. Unattended access, if ever added, is opt-in per device and off by default.
- Always visible: tray icon while the agent runs, amber bar while a session runs. Never add stealth installs or ways to hide the indicator.
- No inbound ports on endpoints, no public RDP, no endpoint-protection bypasses, no remote command shells, scripting or fleet-wide commands.
- The server enforces client isolation and the four permissions (view, control, clipboard, transfer) on every API call and relayed message. Never move a check into the viewer only.
- Audit metadata only. Never log screen images, keystrokes, clipboard contents or file contents.
- Received files are never opened or run automatically.
- Tests and screenshots use fictional data only (`example.test`, `192.0.2.x`, `Contoso Test`).
- Building and documenting a local test launch of the server is in scope. Provisioning servers, deploying to real clients or opening public access is not.

## Folder responsibilities

| Path | Responsibility |
|---|---|
| `cmd/rsm-server` | Server CLI: `init`, `serve`, `fingerprint`, `reset-mfa`, `version` |
| `cmd/rsm-agent` | Windows agent: enroll/install, consent window, session bar, tray, updates. `*_other.go` is a simulator for non-Windows tests |
| `cmd/rsm-viewer` | Native Windows viewer: screen, keyboard capture, menus, file manager, clipboard, update notice |
| `cmd/rsm-release` | Update signing keys and signed manifests |
| `internal/protocol` | Wire format and message types; permission class by type prefix |
| `internal/server` | HTTP API, web console (`web/`, embedded), SQLite store, auth, session hub and relay |
| `internal/agentcore`, `internal/viewercore` | Platform-neutral agent and viewer logic (tested on Linux) |
| `internal/transfer`, `internal/clipsync`, `internal/keys`, `internal/screen`, `internal/pathsafe`, `internal/tlspin` | Engines shared by agent and viewer |
| `internal/update` | Manifest verification, download, staging, install, rollback (platform-neutral, tested) |
| `internal/updateui` | Windows update UI shared by viewer and agent |
| `internal/winapi` | Thin Win32 bindings (windows build tag only) |
| `tests/e2e` | End-to-end test with a real TLS server and simulated agents |
| `scripts` | Root guard, its negative-control test, Windows build scripts |
| `docs` | Feature matrix, component evaluation, security model, testing steps, release notes (`docs/releases/`), images (`docs/images/`) |

## Root files

Allowed tracked root entries are listed in `scripts/root-allowlist.txt` and checked in CI by `scripts/check-root.sh` (git mode with `.git`, filesystem mode in an extracted archive, skipping only `dist`, `build`, `rsm-data`, `.git`, `.vscode`, `.idea`). `scripts/test-check-root.sh` is the negative control. To add a root file: add it to the allowlist in the same pull request and record the reason here. Current exceptions beyond the standard set: `go.mod`, `go.sum` (Go toolchain), `.gitattributes` (line endings, if added).

## Commands

```bash
go vet ./... && GOOS=windows go vet ./...      # windows vet: ignore "possible misuse of unsafe.Pointer"
go test -race ./...
bash scripts/check-root.sh && bash scripts/test-check-root.sh
bash scripts/build-windows.sh 0.1.0-dev [update-public-key]   # cross-build the Windows package into dist/
```

On Windows: `powershell -File scripts\build-windows.ps1 -Version 0.1.0-dev`.

## Workflow rules

- Never commit to `main`. Branches: `feat/`, `fix/`, `docs/`, `chore/`, `release/`. One focused change per PR; merge only with green checks.
- `.github/workflows/delete-merged-branches.yml` deletes a branch after merge when its PR was merged at the branch's current commit, or when `main` contains all its commits and it is over a day old. It never touches `main`, protected branches or branches with open PRs, and lists kept branches with unique commits in the run summary. Manual runs default to a dry run.
- Before any push, inspect the diff for secrets, private data and licensing. Never commit build output, keys, databases or logs.
- Versions: `main.Version` (viewer), `agentcore.Version`, `server.Version`, set together by the build scripts. Documentation-only changes do not bump versions or publish anything. Never overwrite a published release or update file (`rsm-release` refuses).
- Approvals are per task. Record them in DEVELOPMENT_HANDOFF.md as history, never as standing permission.

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
- The relay only forwards message types allowed in each direction (`viewerMay` / `agentMay` in `internal/server/hub.go`); a new message type must be added there.
