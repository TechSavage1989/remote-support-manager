# Development handoff

Current state of Remote Support Manager for the next person or AI session. Read [AGENTS.md](AGENTS.md) first.

## Current version

- Version: **0.1.0-preview.1** (first build, prerelease).
- Repository: https://github.com/TechSavage1989/remote-support-manager (public), default branch `main`.
- Update feed: each self-hosted server serves `/updates/<product>-manifest.json` (+ `.sig`); there is no central feed.

## Code map

See the folder table in [AGENTS.md](AGENTS.md#folder-responsibilities). Entry points: `cmd/rsm-server`, `cmd/rsm-agent`, `cmd/rsm-viewer`, `cmd/rsm-release`.

Protocol: `[4-byte big-endian header length][JSON header with "t"][binary payload]`, header ≤ 1 MB, message ≤ 8 MB. Message type prefixes map to permissions: `scr.` view, `in.` control, `clip.` clipboard, `fs.`/`xf.` transfer. Session control (`session.*`, `ping`, `denied`) needs none.

Session lifecycle: technician creates a session (6-digit code + one-time launch ticket) → agent consent prompt → server marks it active and sends `session.start` → agent opens `/ws/agent/session?sid=` → `session.hello` (epoch 1). Reconnects within 60 s get `session.resumed` (epoch + 1). Ended sessions return HTTP 410 to the viewer.

## Build, test and package

```bash
go vet ./... && GOOS=windows go vet ./...
go test -race ./...
bash scripts/check-root.sh && bash scripts/test-check-root.sh
bash scripts/build-windows.sh 0.1.0-preview.1 [update-public-key]
```

Windows native: `powershell -File scripts\build-windows.ps1 -Version 0.1.0-preview.1`. Requires Go 1.26 (the `go` line in `go.mod`; newer toolchains download automatically).

CI (`.github/workflows/ci.yml`): Linux job (root guard + negative control, gofmt, vet for Linux and Windows, race tests, Windows cross-build) and Windows job (tests, native build, executables start).

## Settings and data paths

| Component | Path |
|---|---|
| Viewer settings | `%LOCALAPPDATA%\RemoteSupportManager\viewer.json` |
| Agent program | `%LOCALAPPDATA%\Programs\Remote Support Manager\RemoteSupportAgent.exe` (HKCU Run key, Settings → Apps entry) |
| Agent settings, credential (DPAPI), log | `%APPDATA%\RemoteSupportManager\agent.json`, `agent.log` |
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
| Tests and build on GitHub's Windows runner | Pending first CI run |
| Native windows on the owner's PC (viewer, agent consent, bar, file manager, clipboard, keyboard) | Pending |
| Two-machine test (technician PC + Windows endpoint VM) | Unverified: no Windows VM available yet; exact steps in [docs/TESTING.md](docs/TESTING.md) |
| Previous-version → new-version update on Windows | Unverified; steps in docs/TESTING.md section 6 |

## Known limitations

- No service component: no Ctrl+Alt+Del (SendSAS needs a service), no sign-in screen or UAC secure desktop.
- GDI capture; DXGI Desktop Duplication would be faster on large screens.
- The update notice is drawn natively with GDI. It has no animation (spinning icon or moving stripes); that also satisfies the reduced-motion rule. Light/dark follows the Windows app theme; contrast checked by colour choice, not by an automated test.
- Release history link points at the GitHub releases page.
- Preview executables are not Authenticode-signed. Azure Trusted Signing is set up by the owner; signing must happen before `rsm-release manifest`.
- No screenshots in the README yet: none have been captured from a real session.

## Decisions and approvals (history, not standing permission)

Task: build the first working version (2026-11-26). The owner approved for this task: creating and publishing the repository as public, pushing, opening the PR and marking it ready, merging after checks, and publishing a prerelease; Remote Control tests on their PC or server; the solution must be free apart from code signing, which they already have in Azure.

## Open items

- License: the repository has no license file, so the default is "all rights reserved". The owner decides.
- Windows endpoint VM for two-machine testing.
- Update signing key: created and kept offline by the owner; the public key is passed to the build. No key exists in the repository.

## Branches

No other branches with unique work.
