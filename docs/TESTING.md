# Testing Remote Support Manager on Windows

Use a technician PC and a separate Windows 10 or 11 **test VM** as the endpoint, with made-up data only. Nothing here opens ports to the internet: the server listens on your own network only if you choose an address for it.

## 1. Automated tests (any OS)

```bash
go vet ./...
go test -race ./...          # Linux/macOS; on Windows without a C compiler use: go test ./...
bash scripts/test-check-root.sh
```

`tests/e2e` starts a real TLS server, three users with MFA, two clients and two simulated agents. It checks inventory and isolation, consent and permissions, screen and input, a 24 MB transfer that is interrupted at 6 MB and resumed, clipboard sync, a transfer blocked by missing permission, ending from the endpoint (the viewer then gets 410), a declined request and device revocation. `TestClientKinds` covers the 0.2 session types: a one-time client joining with a code (the code works once, the customer's unticked permissions are enforced, the device is removed after the session or when the client closes), a support client from the console download (zip contents, two standing sessions, removal by the technician uninstalls it, the other technician cannot remove it) and unattended access (technicians cannot create or remove it, administrators can).

## 2a. Install the server with no command line (0.3)

Unverified on real Windows yet. Use a test PC or VM.

| # | Step | Expected |
|---|---|---|
| S1 | Run `RemoteSupportServerSetup.exe`, press **Yes**, approve the administrator prompt | A message says the server is installed; the browser opens `http://127.0.0.1:8080`; no console window stays open |
| S2 | Services app | *Remote Support Manager Server* is running, startup type Automatic |
| S3 | Setup page: create the administrator (Contoso Test, `admin@example.test`), choose **Only on my own network** | Status shows a console link `https://<pc-name>:8443/`; it opens after the certificate warning |
| S4 | From another PC on the network, open `http://<server-ip>:8080` | Refused or unreachable (setup page is loopback only) |
| S5 | Setup page again: change the organisation with a wrong password | Refused; with the right password it saves and the server restarts |
| S6 | Tailscale installed and signed in on the server; choose **Tailscale Funnel** and save | Status shows `Tailscale Funnel is on` and `https://<pc>.<tailnet>.ts.net/`; if Tailscale asks to allow Funnel, the page shows the link |
| S7 | From a phone on mobile data, open the ts.net address | The console sign-in page, with a valid certificate |
| S8 | Create a one-time session; run the client on a PC on another network | The client connects with no fingerprint and the session works |
| S9 | Router choice (optional): a name pointing at your connection, port 443 forwarded | Status shows `Certificate ready` within a minute or two |
| S10 | Settings → Apps → *Remote Support Manager Server* → Uninstall, answer **No** to deleting data | Service, firewall rule, Start menu link and program folder are gone; `%ProgramData%\Remote Support Manager Server` stays; Tailscale Funnel is turned off |

## 2. Start a local test server from the command line (technician PC)

From the extracted test package (or `dist\` after building):

```powershell
.\rsm-server.exe init --data .\rsm-data --admin-email admin@example.test --hosts "localhost,127.0.0.1,<technician-PC-LAN-IP>"
```

It prints a one-time admin password and the certificate fingerprint. Keep both in the terminal; do not paste them into chats or files.

For a test on one PC, the default address is enough:

```powershell
.\rsm-server.exe serve --data .\rsm-data
```

For a VM endpoint, the VM must reach the technician PC. With VirtualBox, the safest choice is a **Host-only adapter** for the VM; then start the server on the host-only address (for example `192.168.56.1`):

```powershell
.\rsm-server.exe serve --data .\rsm-data --listen 192.168.56.1:8443
```

Windows may ask whether to allow `rsm-server.exe` on networks: allow **Private** networks only. Do not forward the port on your router.

## 3. Console setup

1. Open `https://127.0.0.1:8443` (or the host-only address). The certificate is self-signed: check that the fingerprint your browser shows matches the one printed by `init`, then continue.
2. Sign in as `admin@example.test` with the one-time password, change it, and enrol an authenticator app (scan the QR code).
3. **Administration**: add the client `Contoso Test` (fictional) and the site `Lab` under it.
4. Start the server from the package folder (or pass `--downloads <package>\downloads`) so the console's download links work. If the VM reaches the server at a different address than your browser, add `--public-url https://192.168.56.1:8443`.

## 4. Session types (0.2)

In **New support session**, pick `Contoso Test / Lab`. Open each download link on the VM (copy it over if the VM has no browser access to the console), extract the zip and run `RemoteSupportAgent.exe` as a normal user unless stated.

| # | Check | Expected |
|---|---|---|
| T1 | One-time: create, give the code. On the VM open `https://<server>/download/client`, run it, type the code | The window shows technician, organisation, session name and permissions; untick Clipboard; **Allow**. Nothing appears in Settings → Apps |
| T2 | Connect from the console | Starts without a code prompt; amber bar shows; Clipboard menu is disabled in the viewer |
| T3 | End the session | The VM program closes; the device disappears from Devices (removed). The same code fails if typed again |
| T4 | One-time: close the program before connecting | After about 3 minutes the device is removed |
| T5 | Support: create, run the download on the VM, **Allow and install** | Tray icon; *Remote Support Manager agent* in Settings → Apps; survives sign-out and sign-in |
| T6 | Connect, end, connect again | Each session starts without a prompt and shows the bar |
| T7 | **Close support session** in the console | The VM shows "Remote support for this PC was closed"; within a minute the tray icon, Apps entry, `%LOCALAPPDATA%\Programs\Remote Support Manager` and `agent.json` are gone |
| T8 | Unattended (as admin): create, run the download as a normal user, tick the confirmation, **Install…** | UAC prompt; after approval a tray notice for the signed-in user, service *Remote Support Manager (unattended access)* running, Apps entry |
| T9 | Lock the VM (Win+L) and connect | The lock screen is visible and usable; Keyboard → Ctrl+Alt+Del works; the bar shows once signed in |
| T10 | Sign in as another Windows user | The tray notice shows for them too |
| T11 | **Remove device and uninstall** | Service, tray notice, Apps entry, `%ProgramFiles%\Remote Support Manager Access` and `%ProgramData%\Remote Support Manager Access` are removed; the SoftwareSASGeneration policy is restored |
| T12 | Technician account | Cannot pick unattended or attended types; cannot remove an unattended device |
| T13 | Viewer installer | **New support session → Download viewer installer**: installs without admin rights, Start menu entry, opens `rsm://` links; Settings → Apps → *Remote Support Manager Viewer* → Uninstall removes it. The portable viewer runs without installing |

## 5. Attended agent from the command line

The command-line enrolment from 0.1 still works:

1. **Administration → Enroll a device from the command line**: create a token for the site. Copy the command it shows.

## 5a. Enrol the endpoint VM

1. Copy `downloads\RemoteSupportAgent.exe` to the VM.
2. In a normal (non-admin) PowerShell on the VM, run the copied command, for example:
   ```powershell
   .\RemoteSupportAgent.exe enroll --server https://192.168.56.1:8443 --token rsm-enroll-... --pin <fingerprint>
   ```
3. Confirm the prompt. Expected: "This PC is enrolled", a tray icon, and an entry in **Settings → Apps** named *Remote Support Manager agent*.
4. In the console the device appears under Contoso Test → Lab as **Online**, with Windows version, uptime, disks and agent version.

## 5b. Attended session checks

On the technician PC, open the device and press **Connect** with all permissions ticked, then **Open in viewer**. The first time, the viewer asks to handle `rsm://` links for your account.

| # | Check | Expected |
|---|---|---|
| 1 | Consent | The VM shows the technician's name and email and asks for the code; the viewer shows the 6-digit code to read out |
| 2 | Wrong code | "That code was not correct"; after 3 wrong codes the request is declined |
| 3 | Untick Transfer, accept | Session starts; the viewer's Files menu is disabled; the amber bar shows the permissions |
| 4 | Screen | The VM desktop appears; View → Fit, Actual size, Full screen (Ctrl+Alt+F11) work; Monitor lists each screen |
| 5 | Keyboard | With "Send shortcuts" on, Alt+Tab and the Windows key act on the VM; Ctrl+Alt+F12 turns it off; alt-tabbing away releases keys |
| 6 | Toolbar keys | Keyboard menu → Win+R opens Run on the VM; Ctrl+Alt+Del explains it needs unattended access |
| 7 | Clipboard | Clipboard → Sync; copy Unicode text (e.g. `Grüße 你好 ✓`) and an image both ways; Paste as keystrokes types text |
| 8 | Remove permission | On the VM, bar → Permissions… → untick Clipboard; sync stops and the viewer says so |
| 9 | Files | New session with Transfer: send a folder with an empty subfolder and a 1 GB file; cancel and resume; send again and choose Keep both; receive a file; confirm received files are not opened |
| 10 | Reconnect | Disconnect the VM network for 20 s and reconnect: the session resumes; held keys were released |
| 11 | End | End session on the VM bar: the viewer says it ended; opening the old link again fails |
| 12 | Isolation | Sign in as a technician assigned only to another client: the device is not visible and its URL returns "not found" |
| 13 | Removal | Device → **Remove device and uninstall** (admin): the agent says support was closed and removes itself |
| 14 | Audit | Admin → Audit lists sessions, consents, permission changes and transfers (names and sizes only) |

## 6. Updater checks

1. Create a signing key: `rsm-release.exe keygen --out C:\keys\update-signing.key` (keep it out of the repository). Build version `0.1.1-test` with the printed public key: `scripts\build-windows.ps1 -Version 0.1.1-test -UpdatePublicKey <key>`, and version `0.1.0-test` the same way.
2. Publish 0.1.1: `rsm-release.exe manifest --key C:\keys\update-signing.key --product viewer --version 0.1.1-test --file dist\downloads\RemoteSupportViewer.exe --out .\rsm-data\updates` and restart the server with `--updates .\rsm-data\updates`.
3. Run the 0.1.0 viewer, connect once, then wait a few seconds: the notice in the bottom-right says **Update available**. Nothing downloads by itself.
4. During an active session press **Download update**, then **Review and restart** → **Install and restart**: it refuses and says the session is active.
5. End the session and install: the viewer restarts as 0.1.1; `RemoteSupportViewer.exe.previous` exists as the rollback copy.
6. Tamper test: replace the published exe with another file: the download is rejected as not matching.

## 7. Uninstall

- Endpoint: close the support session or remove the device in the console (the client uninstalls itself), or **Settings → Apps → Remote Support Manager agent** / **Remote Support Manager (unattended access)** → Uninstall. One-time sessions leave nothing behind except the downloaded zip.
- Technician PC: **Settings → Apps → Remote Support Manager Viewer → Uninstall**. For the portable viewer, run `RemoteSupportViewer.exe --unregister` if you registered `rsm://` links, then delete it and `%LOCALAPPDATA%\RemoteSupportManager\viewer.json`.
- Server: stop `rsm-server.exe` and delete its data folder.
