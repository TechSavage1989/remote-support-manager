# Security model

## Trust boundaries

- **Server** (self-hosted by you): the only component that accepts connections. TLS only. On your own network it uses a self-signed certificate pinned by SPKI SHA-256 in agents and viewers. Over the internet it uses a publicly trusted certificate: from Let's Encrypt (router mode, TLS-ALPN challenge on port 443) or held by the tunnel (Tailscale Funnel or Cloudflare Tunnel, which forward to `127.0.0.1:8443`); clients then verify it against the Windows certificate store.
- **Setup page** (`rsm-server run`, the Windows service): plain HTTP on `127.0.0.1:8080` only. It refuses non-loopback clients, Host headers other than localhost/127.0.0.1 (DNS rebinding) and posts whose `Origin` or `Sec-Fetch-Site` show another site. It creates the first administrator; after that every change needs an administrator's email and password (5 failures pause it for a minute) and is audited.
- **Behind a tunnel**, every connection arrives from the loopback address, so the client address for rate limits and the audit log is taken from the last `X-Forwarded-For` entry, and only for loopback peers.
- **Client** (endpoint): makes outbound connections only, never listens. The server keeps only a SHA-256 hash of its device credential.
  - One-time: runs as the signed-in user from wherever it was downloaded; the credential stays in memory.
  - Support and attended: run as the signed-in user; credential stored with user-scope DPAPI in `%LOCALAPPDATA%`.
  - Unattended access: a LocalSystem service. The credential is machine-scope DPAPI in `%ProgramData%\Remote Support Manager Access`, whose ACL allows only SYSTEM and Administrators. Sessions run in a LocalSystem helper in the console session.
- **Viewer** (technician): starts from a one-time launch ticket in the `rsm://` link. The link never carries a password or session cookie.

## Accounts

- Passwords: bcrypt (cost 12), at least 12 characters. Unknown accounts take the same time to reject.
- MFA: TOTP (RFC 6238) is required for every console user, with a replay guard.
- Login is limited per IP (10 per minute) and an account locks for 15 minutes after 5 failures.
- Web sessions: HttpOnly, Secure, SameSite=Strict cookie plus a CSRF header; strict Content-Security-Policy.

## Authorisation

- Technicians are assigned to clients with separate **view**, **control**, **clipboard** and **transfer** permissions. The server checks them on every API call and on every relayed message; requests for another client's devices return "not found" and are audited.
- The endpoint user can remove permissions before and during a session. Permissions can never be added to a running session.
- The relay enforces message direction: an endpoint can never push files to the technician unless the technician requested them.

## Consent and visibility

- Invitations are single use and short lived (one-time 30 min, support 24 h, unattended 72 h by default); the server stores only hashes of tokens and codes. Lookups are rate-limited per IP.
- Before anything connects, the client shows the organisation, technician, the name of the session, how it will run and the permissions asked for.
  - Attended agent: every session also needs the endpoint user to accept and type a fresh 6-digit code (3 attempts, expires).
  - One-time and support: the endpoint user agrees once and can untick control, clipboard and transfer. The server stores what they allowed and grants each session at most that.
  - Unattended access: only console administrators can create the installer; installing needs a Windows administrator (UAC) and an explicit "I am allowed to approve" tick.
- An amber bar stays on top during every session with End session and Permissions…; a tray icon is visible while the client runs. Unattended access adds a tray notice for every signed-in user and a Settings > Apps entry.
- Closing a support session or removing a device revokes its credential and tells the client to uninstall itself (sign-in entry or service, Apps entry, settings, credential and program folder). A client that finds its credential rejected does the same.

## Data handling

- Audit records hold metadata only: who, when, which device, permissions, transfer file names and sizes, clipboard item counts. Never screen images, keystrokes, clipboard contents or file contents.
- Received files are written with the Mark of the Web and never opened or run automatically.

## Updates

- Manifests are signed with Ed25519; the public key is built into the apps. Packages are checked for size, SHA-256 and the Windows executable header before staging.
- Downloading and installing each require the user to press a button. Installing is refused during a session. One previous version is kept and restored automatically if the new version does not start.
- Release executables are Authenticode-signed with Azure Artifact Signing before the installers embed them and before the manifest is created; the manifest hash covers the signed file (docs/CODE_SIGNING.md).

## Deliberately not built

Remote command shells, scripting or fleet-wide commands, stealth installs, hiding the indicator, inbound ports on endpoints, public RDP, and anything that bypasses endpoint protection.
