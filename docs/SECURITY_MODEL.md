# Security model

## Trust boundaries

- **Server** (self-hosted by you): the only component that accepts connections. TLS only, self-signed certificate pinned by SPKI SHA-256 in agents and viewers. Listens on `127.0.0.1:8443` unless you choose another address.
- **Agent** (endpoint): runs as the signed-in user, makes outbound connections only, never listens. Its device credential is stored with Windows DPAPI; the server keeps only a SHA-256 hash.
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

- Every session needs the endpoint user to accept and type the technician's 6-digit code (3 attempts, expires).
- An amber bar stays on top during a session with End session and Permissions…; the tray icon is always visible while the agent runs.
- Unattended access is not implemented. If added later it must be opt-in per device and off by default.

## Data handling

- Audit records hold metadata only: who, when, which device, permissions, transfer file names and sizes, clipboard item counts. Never screen images, keystrokes, clipboard contents or file contents.
- Received files are written with the Mark of the Web and never opened or run automatically.

## Updates

- Manifests are signed with Ed25519; the public key is built into the apps. Packages are checked for size, SHA-256 and the Windows executable header before staging.
- Downloading and installing each require the user to press a button. Installing is refused during a session. One previous version is kept and restored automatically if the new version does not start.
- Authenticode signing (for example Azure Trusted Signing) is applied to the executables before the manifest is created; the manifest hash covers the signed file.

## Deliberately not built

Remote command shells, scripting or fleet-wide commands, stealth installs, hiding the indicator, inbound ports on endpoints, public RDP, and anything that bypasses endpoint protection.
