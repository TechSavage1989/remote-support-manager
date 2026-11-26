# Testing Remote Support Manager on Windows

Use a technician PC and a separate Windows 10 or 11 **test VM** as the endpoint, with made-up data only. Nothing here opens ports to the internet: the server listens on your own network only if you choose an address for it.

## 1. Automated tests (any OS)

```bash
go vet ./...
go test -race ./...          # Linux/macOS; on Windows without a C compiler use: go test ./...
bash scripts/test-check-root.sh
```

`tests/e2e` starts a real TLS server, three users with MFA, two clients and two simulated agents. It checks inventory and isolation, consent and permissions, screen and input, a 24 MB transfer that is interrupted at 6 MB and resumed, clipboard sync, a transfer blocked by missing permission, ending from the endpoint (the viewer then gets 410), a declined request and device revocation.

## 2. Start a local test server (technician PC)

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
3. **Admin → Clients**: add `Contoso Test` (fictional). **Sites**: add `Lab` under it.
4. **Admin → Enrollment**: create a token for that site. Copy the command it shows.

## 4. Enrol the endpoint VM

1. Copy `RemoteSupportAgent.exe` to the VM.
2. In a normal (non-admin) PowerShell on the VM, run the copied command, for example:
   ```powershell
   .\RemoteSupportAgent.exe enroll --server https://192.168.56.1:8443 --token rsm-enroll-... --pin <fingerprint>
   ```
3. Confirm the prompt. Expected: "This PC is enrolled", a tray icon, and an entry in **Settings → Apps** named *Remote Support Manager agent*.
4. In the console the device appears under Contoso Test → Lab as **Online**, with Windows version, uptime, disks and agent version.

## 5. Attended session checks

On the technician PC, open the device and press **Connect** with all permissions ticked, then **Open in viewer**. The first time, the viewer asks to handle `rsm://` links for your account.

| # | Check | Expected |
|---|---|---|
| 1 | Consent | The VM shows the technician's name and email and asks for the code; the viewer shows the 6-digit code to read out |
| 2 | Wrong code | "That code was not correct"; after 3 wrong codes the request is declined |
| 3 | Untick Transfer, accept | Session starts; the viewer's Files menu is disabled; the amber bar shows the permissions |
| 4 | Screen | The VM desktop appears; View → Fit, Actual size, Full screen (Ctrl+Alt+F11) work; Monitor lists each screen |
| 5 | Keyboard | With "Send shortcuts" on, Alt+Tab and the Windows key act on the VM; Ctrl+Alt+F12 turns it off; alt-tabbing away releases keys |
| 6 | Toolbar keys | Keyboard menu → Win+R opens Run on the VM; Ctrl+Alt+Del shows the explanation that it needs the service component |
| 7 | Clipboard | Clipboard → Sync; copy Unicode text (e.g. `Grüße 你好 ✓`) and an image both ways; Paste as keystrokes types text |
| 8 | Remove permission | On the VM, bar → Permissions… → untick Clipboard; sync stops and the viewer says so |
| 9 | Files | New session with Transfer: send a folder with an empty subfolder and a 1 GB file; cancel and resume; send again and choose Keep both; receive a file; confirm received files are not opened |
| 10 | Reconnect | Disconnect the VM network for 20 s and reconnect: the session resumes; held keys were released |
| 11 | End | End session on the VM bar: the viewer says it ended; opening the old link again fails |
| 12 | Isolation | Sign in as a technician assigned only to another client: the device is not visible and its URL returns "not found" |
| 13 | Revocation | Admin → revoke the device: the agent stops and says access was revoked |
| 14 | Audit | Admin → Audit lists sessions, consents, permission changes and transfers (names and sizes only) |

## 6. Updater checks

1. Create a signing key: `rsm-release.exe keygen --out C:\keys\update-signing.key` (keep it out of the repository). Build version `0.1.1-test` with the printed public key: `scripts\build-windows.ps1 -Version 0.1.1-test -UpdatePublicKey <key>`, and version `0.1.0-test` the same way.
2. Publish 0.1.1: `rsm-release.exe manifest --key C:\keys\update-signing.key --product viewer --version 0.1.1-test --file dist\RemoteSupportViewer.exe --out .\rsm-data\updates` and restart the server with `--updates .\rsm-data\updates`.
3. Run the 0.1.0 viewer, connect once, then wait a few seconds: the notice in the bottom-right says **Update available**. Nothing downloads by itself.
4. During an active session press **Download update**, then **Review and restart** → **Install and restart**: it refuses and says the session is active.
5. End the session and install: the viewer restarts as 0.1.1; `RemoteSupportViewer.exe.previous` exists as the rollback copy.
6. Tamper test: replace the published exe with another file: the download is rejected as not matching.

## 7. Uninstall

- Endpoint: **Settings → Apps → Remote Support Manager agent → Uninstall**, or `"%LOCALAPPDATA%\Programs\Remote Support Manager\RemoteSupportAgent.exe" uninstall`. Then delete `%APPDATA%\RemoteSupportManager` if anything is left.
- Technician PC: delete the viewer folder and `%LOCALAPPDATA%\RemoteSupportManager`. If you registered `rsm://` links, run `RemoteSupportViewer.exe --unregister` first.
- Server: stop `rsm-server.exe` and delete its data folder.
