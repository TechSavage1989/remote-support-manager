<div align="center">

<h1>Remote Support Manager</h1>

<p>Self-hosted, Windows-first remote support: one-time, ongoing and unattended, always visible to the person at the PC.</p>

<p><a href="#download">Download</a> · <a href="#getting-started">Get started</a> · <a href="#features">Features</a> · <a href="#updates-and-settings">Updates</a> · <a href="#development">Development</a></p>

</div>

> **Preview.** This is an early test build. Use it with test VMs and made-up data. From 0.3.0-preview.2 the executables are code-signed with Azure Artifact Signing.

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
- **Security**: MFA for every console user, outbound-only TLS (a public certificate over the internet, or a pinned one on your own network), single-use invitations, per-client permissions enforced by the server, and an audit log of invitations, consent, sessions and removals that never records contents.
- **Safe updates**: signed, user-approved updates from your own server, never during a session, with automatic rollback.

Not included on purpose: remote command shells or scripting, fleet-wide commands, stealth or hidden installs, and inbound ports on endpoints. See [docs/FEATURE_MATRIX.md](docs/FEATURE_MATRIX.md).

## Download

Preview builds are on the [releases page](https://github.com/TechSavage1989/remote-support-manager/releases) (marked *Pre-release*). Download `RemoteSupportManager-<version>-windows-x64.zip` and check it against the `.sha256` file next to it.

The zip contains:

| File | Where it runs |
|---|---|
| `RemoteSupportServerSetup.exe` | **Start here.** The computer that will be your server (it can be your own PC): installs the server and web console, no command line |
| `rsm-server.exe` | The same server, for people who prefer the command line |
| `rsm-release.exe` | Administrators: update signing |
| `downloads\RemoteSupportViewerSetup.exe` | Technician PC: installs the viewer for your Windows account |
| `downloads\RemoteSupportViewer.exe` | Technician PC: the same viewer, portable |
| `downloads\RemoteSupportAgent.exe` | Customer PCs. You don't hand this out directly: the console wraps it with the right settings for each support session. |

`rsm-server.exe` serves the `downloads` folder next to it, so the console's download links work without extra setup.

## Requirements

- Windows 10 or 11, x64, for the client and viewer. One-time sessions install nothing; support sessions and the viewer install for the current user without administrator rights; unattended access needs a Windows administrator.
- The server runs on any Windows 10/11 or Windows Server computer that stays on, including your own PC (or on Linux/macOS with `rsm-server run`). Administrator rights are needed once, to install it.
- To work over the internet, one of these, all free: a free [Tailscale](https://tailscale.com) account (no router changes), your own internet name with port 443 forwarded on your router, or a Cloudflare Tunnel with your own domain. Customers' PCs only connect out, so nothing is opened on their side.
- Tested so far: the automated tests pass on Linux and on GitHub's Windows runner. Native window checks are listed in [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md#validation).

## Getting started

1. On the computer that will be your server, run **RemoteSupportServerSetup.exe**, press **Yes**, and approve the Windows administrator prompt. The server is installed as a background service and a setup page opens in your browser (later: Start menu → *Remote Support Manager Server setup*).
2. On the setup page, create your administrator account, type your organisation name and choose how customers reach the server:
   - **Tailscale Funnel** (free, no router changes): install Tailscale, sign in with a free account, reload the page and choose it. Your address looks like `yourpc.tail1234.ts.net`.
   - **My own address and router**: type an internet name that points at your connection (a free DuckDNS name works) and forward port 443 on your router to this computer. The server gets a free Let's Encrypt certificate by itself.
   - **Cloudflare Tunnel**: point your tunnel at `https://127.0.0.1:8443` with *No TLS Verify* on.
   - **Only on my own network**: no internet access; browsers warn about the server's own certificate the first time.
3. Open the console link the setup page shows, sign in and set up two-step sign-in (MFA) with an authenticator app.
4. In **Administration**, add a client and a site.
5. In **New support session**, pick the site and the type, give it a name and press **Create**. Send the customer the download link (and, for one-time sessions, the code).
6. On the customer PC they download and run it, see who is asking and what for, and agree. The PC appears under **Devices**.

Command-line setup (`rsm-server init` / `serve`) still works; see [docs/TESTING.md](docs/TESTING.md).

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

- **Customers can't connect over the internet**: open the setup page on the server (Start menu → *Remote Support Manager Server setup*); its Status box says whether the certificate or Tailscale Funnel is ready and what's wrong. For the router choice, check that the name points at your internet address and port 443 is forwarded. If Tailscale asks you to allow Funnel, the page shows the link.
- **The agent cannot connect on a local network**: check the server address and fingerprint, and that the endpoint can reach the server's port 8443.
- **Ctrl+Alt+Del says it is unavailable**: Windows only accepts it from a service, so it works with unattended access only. Use Ctrl+Shift+Esc for Task Manager.
- **No picture of the sign-in screen or UAC prompts**: same reason; unattended access follows those screens.
- **"This support link can no longer be used"**: invitations work once and expire (one-time 30 minutes, support 24 hours, unattended 72 hours). Create a new one.
- **Uninstall by hand**: Settings → Apps → *Remote Support Manager agent*, *Remote Support Manager Viewer*, *Remote Support Manager (unattended access)* or *Remote Support Manager Server* (it asks whether to keep the server's data).

## Development

Read [AGENTS.md](AGENTS.md) and [DEVELOPMENT_HANDOFF.md](DEVELOPMENT_HANDOFF.md) first.

```bash
go test -race ./...
bash scripts/build-windows.sh 0.2.0-dev      # cross-builds dist/RemoteSupportManager-0.2.0-dev-windows-x64.zip
```

Design notes: [security model](docs/SECURITY_MODEL.md), [component evaluation](docs/COMPONENT_EVALUATION.md), [feature matrix](docs/FEATURE_MATRIX.md).
