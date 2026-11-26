<div align="center">

<h1>Remote Support Manager</h1>

<p>Self-hosted, Windows-first remote support: one-time, ongoing and unattended, always visible to the person at the PC.</p>

<p><a href="#download">Download</a> · <a href="#getting-started">Get started</a> · <a href="#features">Features</a> · <a href="#updates-and-settings">Updates</a> · <a href="#development">Development</a></p>

</div>

> **Preview.** This is an early test build. Use it with test VMs and made-up data. The executables in the preview package are not Authenticode-signed.

## Features

For IT technicians who support Windows PCs and want to run the whole thing on their own server.

- **Four ways to connect**, chosen when you create the download in the console:

  | Type | Installed? | Each session | Ends when |
  |---|---|---|---|
  | **One-time (portable)** | No. The customer runs the program and types a 6-digit code. | Starts without a prompt; they already agreed. | The session ends. Nothing is left behind. |
  | **Support session** | Per user, no admin rights, after the customer agrees once. | Starts without a prompt, as often as needed. | You press **Close support session**: it uninstalls itself from the PC. |
  | **Unattended access** | Machine-wide Windows service, with Windows administrator approval. Administrators only. | Starts without a prompt, also at the sign-in screen. | An administrator removes the device: it uninstalls itself. |
  | **Attended agent** | Per user. | The customer accepts and types your code every time. | An administrator removes the device. |

- **Always visible**: a tray icon while the client runs and an amber bar with **End session** and **Permissions…** during every session, whatever the type. Unattended access also shows a tray notice to everyone who signs in.
- **The customer decides what you may do**: view (always), control, clipboard and file transfer. For one-time and support sessions they can untick anything when they agree; the server never grants more.
- **Inventory**: Client → Site → Device tree with search, online status, last seen, Windows version, uptime, disks, client type and agent version.
- **Native Windows viewer**, as an installer (per user, Start menu) or a portable .exe: fit, actual size, full screen, monitor switching, and a "send shortcuts" mode so Alt+Tab and the Windows key go to the remote PC (Ctrl+Alt+F12 always gets you out).
- **Keyboard menu**: Ctrl+Alt+Del (unattended access), Ctrl+Shift+Esc, Alt+Tab, Win, Win+R, Win+L, Alt+F4, Print Screen.
- **File manager**: two panes, multi-select, drag and drop from Explorer, whole folders, progress, cancel and resume, Replace / Keep both / Skip, SHA-256 check per file. Received files are never opened automatically.
- **Clipboard**: text in any language, rich text and images both ways, direction control, Paste as keystrokes.
- **Security**: MFA for every console user, outbound-only TLS with certificate pinning, single-use invitations, per-client permissions enforced by the server, and an audit log of invitations, consent, sessions and removals that never records contents.
- **Safe updates**: signed, user-approved updates from your own server, never during a session, with automatic rollback.

Not included on purpose: remote command shells or scripting, fleet-wide commands, stealth or hidden installs, and inbound ports on endpoints. See [docs/FEATURE_MATRIX.md](docs/FEATURE_MATRIX.md).

## Download

Preview builds are on the [releases page](https://github.com/TechSavage1989/remote-support-manager/releases) (marked *Pre-release*). Download `RemoteSupportManager-<version>-windows-x64.zip` and check it against the `.sha256` file next to it.

The zip contains:

| File | Where it runs |
|---|---|
| `rsm-server.exe` | Your server or technician PC: server and web console |
| `rsm-release.exe` | Administrators: update signing |
| `downloads\RemoteSupportViewerSetup.exe` | Technician PC: installs the viewer for your Windows account |
| `downloads\RemoteSupportViewer.exe` | Technician PC: the same viewer, portable |
| `downloads\RemoteSupportAgent.exe` | Customer PCs. You don't hand this out directly: the console wraps it with the right settings for each support session. |

`rsm-server.exe` serves the `downloads` folder next to it, so the console's download links work without extra setup.

## Requirements

- Windows 10 or 11, x64, for the client and viewer. One-time sessions install nothing; support sessions and the viewer install for the current user without administrator rights; unattended access needs a Windows administrator.
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
3. In **Administration**, add a client and a site.
4. In **New support session**, pick the site and the type, give it a name and press **Create**. The download link (and, for one-time sessions, the code) appears.
5. On the customer PC, download the zip, extract it and run `RemoteSupportAgent.exe`. They see who is asking and what for, and agree. The PC appears under **Devices**.

To reach endpoints on other machines, start the server on an address they can reach, for example `--listen 192.168.56.1:8443`, and include it in `init --hosts`. Full steps, including a VirtualBox host-only setup: [docs/TESTING.md](docs/TESTING.md).

## How to use

1. **Devices** → pick the PC, choose the permissions and press **Connect**, then **Open in viewer**.
   - Attended agent: read the 6-digit code to the person at the PC; they type it and press **Allow**.
   - One-time, support and unattended: the session starts straight away, within what the customer allowed.
2. In the viewer:
   - **View** menu: Fit, Actual size, Full screen (Ctrl+Alt+F11), Monitor.
   - **Keyboard** menu: send shortcuts on/off, release held keys, special keys.
   - **Clipboard** menu: Sync, direction, Paste as keystrokes.
   - **Session → Files…**: file manager. Files you receive go to the folder you choose (Downloads by default).
3. Either side can end a session. The person at the PC can remove permissions at any time from the bar.
4. When the job is done, press **Close support session** (one-time and support) or **Remove device and uninstall** (administrators, for unattended and attended). The client uninstalls itself the next time it is online.

Technicians can create one-time and support sessions for the clients they are assigned to. Unattended access and attended agents need an administrator.

## Updates and settings

- The viewer and agent check your server for updates shortly after starting, every 5 minutes, and when the viewer regains focus. Checking never downloads anything.
- When an update is available, a notice appears in the viewer's bottom-right corner (the agent shows a tray notification). Press **Download update**, then **Review and restart** and **Install and restart**. Installing waits if a session is active.
- **Help → App updates…** (viewer) or the tray's **App updates…** (agent) shows the same card, a **Check for updates** button, the automatic-check option and how downloads are verified.
- The previous version is kept and restored automatically if the new one does not start.
- Settings: viewer `%LOCALAPPDATA%\RemoteSupportManager\viewer.json`; support and attended clients `%LOCALAPPDATA%\RemoteSupportManager\agent.json` (credential protected with DPAPI) and `agent.log`; unattended access `%ProgramData%\Remote Support Manager Access\` (administrators and SYSTEM only). One-time sessions keep nothing on disk.
- Unattended access does not update itself yet; install a newer download over it.

Release notes: [docs/releases](docs/releases).

## Troubleshooting

- **The agent cannot connect**: check the server address and `--pin` fingerprint, and that the server listens on an address the endpoint can reach. Allow `rsm-server.exe` on **Private** networks only.
- **Ctrl+Alt+Del says it is unavailable**: Windows only accepts it from a service, so it works with unattended access only. Use Ctrl+Shift+Esc for Task Manager.
- **No picture of the sign-in screen or UAC prompts**: same reason; unattended access follows those screens.
- **"This support link can no longer be used"**: invitations work once and expire (one-time 30 minutes, support 24 hours, unattended 72 hours). Create a new one.
- **Uninstall by hand**: Settings → Apps → *Remote Support Manager agent*, *Remote Support Manager Viewer* or *Remote Support Manager (unattended access)*.

## Development

Read [AGENTS.md](AGENTS.md) and [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md) first.

```bash
go test -race ./...
bash scripts/build-windows.sh 0.2.0-dev      # cross-builds dist/RemoteSupportManager-0.2.0-dev-windows-x64.zip
```

Design notes: [security model](docs/SECURITY_MODEL.md), [component evaluation](docs/COMPONENT_EVALUATION.md), [feature matrix](docs/FEATURE_MATRIX.md).
