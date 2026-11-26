<div align="center">

<h1>Remote Support Manager</h1>

<p>Self-hosted, Windows-first attended remote support with a device inventory.</p>

<p><a href="#download">Download</a> · <a href="#getting-started">Get started</a> · <a href="#features">Features</a> · <a href="#updates-and-settings">Updates</a> · <a href="#development">Development</a></p>

</div>

> **Preview.** This is an early test build. Use it with test VMs and made-up data. The executables in the preview package are not Authenticode-signed.

## Features

For IT technicians who support Windows PCs and want to run the whole thing on their own server.

- **Inventory**: Client → Site → Device tree with search, online status, last seen, Windows version, uptime, disks and agent version.
- **Attended sessions only**: the person at the PC sees your name and email, types your 6-digit support code and chooses what you may do. An amber bar stays on their screen with **End session** and **Permissions…**.
- **Native Windows viewer**: fit, actual size, full screen, monitor switching, and a "send shortcuts" mode so Alt+Tab and the Windows key go to the remote PC (Ctrl+Alt+F12 always gets you out).
- **Keyboard menu**: Ctrl+Alt+Del, Ctrl+Shift+Esc, Alt+Tab, Win, Win+R, Win+L, Alt+F4, Print Screen.
- **File manager**: two panes, multi-select, drag and drop from Explorer, whole folders (including empty ones), progress, cancel and resume, Replace / Keep both / Skip, SHA-256 check per file. Received files are never opened automatically.
- **Clipboard**: text in any language, rich text and images both ways, direction control, Paste as keystrokes. Copied files go through the transfer queue.
- **Security**: MFA for every console user, outbound-only TLS with certificate pinning, one-time enrollment, device revocation, per-client view / control / clipboard / transfer permissions enforced by the server, and an audit log that never records contents.
- **Safe updates**: signed, user-approved updates from your own server, never during a session, with automatic rollback.

Not included on purpose: unattended access, remote command shells or scripting, stealth installs, and inbound ports on endpoints. See [docs/FEATURE_MATRIX.md](docs/FEATURE_MATRIX.md).

## Download

Preview builds are on the [releases page](https://github.com/TechSavage1989/remote-support-manager/releases) (marked *Pre-release*). Download `RemoteSupportManager-<version>-windows-x64.zip` and check it against the `.sha256` file next to it.

The zip contains:

| File | Where it runs |
|---|---|
| `rsm-server.exe` | Your server or technician PC: server and web console |
| `RemoteSupportViewer.exe` | Technician PC |
| `RemoteSupportAgent.exe` | Each endpoint you support |
| `rsm-release.exe` | Administrators: update signing |

## Requirements

- Windows 10 or 11, x64, for the agent and viewer. The agent installs for the current user; no administrator rights needed.
- The server runs on Windows (or Linux/macOS when built from source). It needs one TCP port reachable by your endpoints on a network you control.
- Tested so far: the automated tests pass on Linux and on GitHub's Windows runner. Native window checks are listed in [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md#validation).

## Getting started

1. Extract the zip on the technician PC and run, in PowerShell:
   ```powershell
   .\rsm-server.exe init --data .\rsm-data --admin-email admin@example.test
   .\rsm-server.exe serve --data .\rsm-data
   ```
   `init` prints a one-time password and the certificate fingerprint.
2. Open `https://127.0.0.1:8443`, check the certificate fingerprint matches, sign in, change the password and set up MFA.
3. In **Admin**, add a client and a site, then create an enrollment token and copy the command it shows.
4. Run that command on the endpoint (`RemoteSupportAgent.exe enroll --server … --token … --pin …`) and confirm. The PC appears in the console as **Online**.

To reach endpoints on other machines, start the server on an address they can reach, for example `--listen 192.168.56.1:8443`, and include it in `init --hosts`. Full steps, including a VirtualBox host-only setup: [docs/TESTING.md](docs/TESTING.md).

## How to use

1. In the console, open a device, choose the permissions for this session and press **Connect**.
2. Press **Open in viewer** and read the 6-digit code to the person at the PC. They type it and press **Allow**.
3. In the viewer:
   - **View** menu: Fit, Actual size, Full screen (Ctrl+Alt+F11), Monitor.
   - **Keyboard** menu: send shortcuts on/off, release held keys, special keys.
   - **Clipboard** menu: Sync, direction, Paste as keystrokes.
   - **Session → Files…**: file manager. Files you receive go to the folder you choose (Downloads by default).
4. Either side can end the session. The person at the PC can remove permissions at any time from the bar.

## Updates and settings

- The viewer and agent check your server for updates shortly after starting, every 5 minutes, and when the viewer regains focus. Checking never downloads anything.
- When an update is available, a notice appears in the viewer's bottom-right corner (the agent shows a tray notification). Press **Download update**, then **Review and restart** and **Install and restart**. Installing waits if a session is active.
- **Help → App updates…** (viewer) or the tray's **App updates…** (agent) shows the same card, a **Check for updates** button, the automatic-check option and how downloads are verified.
- The previous version is kept and restored automatically if the new one does not start.
- Settings: viewer `%LOCALAPPDATA%\RemoteSupportManager\viewer.json`; agent `%APPDATA%\RemoteSupportManager\agent.json` (credential protected with DPAPI) and `agent.log`.

Release notes: [docs/releases](docs/releases).

## Troubleshooting

- **The agent cannot connect**: check the server address and `--pin` fingerprint, and that the server listens on an address the endpoint can reach. Allow `rsm-server.exe` on **Private** networks only.
- **Ctrl+Alt+Del says it is unavailable**: Windows only accepts it from a service component, which this preview does not include. Use Ctrl+Shift+Esc for Task Manager.
- **No picture of the sign-in screen or UAC prompts**: same reason; planned for a later release.
- **Uninstall the agent**: Settings → Apps → *Remote Support Manager agent*, or run `RemoteSupportAgent.exe uninstall` from `%LOCALAPPDATA%\Programs\Remote Support Manager`.

## Development

Read [AGENTS.md](AGENTS.md) and [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md) first.

```bash
go test -race ./...
bash scripts/build-windows.sh 0.1.0-dev      # cross-builds dist/RemoteSupportManager-0.1.0-dev-windows-x64.zip
```

Design notes: [security model](docs/SECURITY_MODEL.md), [component evaluation](docs/COMPONENT_EVALUATION.md), [feature matrix](docs/FEATURE_MATRIX.md).
