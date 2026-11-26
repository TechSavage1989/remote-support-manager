# Feature matrix

ScreenConnect's public documentation was used only as a list of capabilities that support technicians expect. No code, assets, wording or branding were copied. Everything here is implemented from scratch on Windows APIs and Go's standard library.

Legend: **First build** = in this release and covered by tests (see [TESTING.md](TESTING.md)); **Later** = planned, not built; **Out of scope** = deliberately not built.

| Area | Capability | Status | Notes |
|---|---|---|---|
| Inventory | Client → Site → Device tree with search | First build | Server-side isolation: technicians only see clients they are assigned to |
| Inventory | Online status, last seen, Windows version, uptime, disks, agent version | First build | Reported by the agent every 60 s |
| Attended support | Short-lived 6-digit support code, technician name and email shown before consent | First build | Code expires with the request (default 5 min); 3 attempts |
| Attended support | Endpoint user can untick permissions before accepting | First build | View is always required; control, clipboard, transfer optional |
| Session | Always-visible amber session bar and tray icon on the endpoint | First build | The bar cannot be closed while a session runs |
| Session | Disconnect from either side | First build | End session on the bar, viewer menu or console |
| Session | Safe reconnect within 60 s; ended or revoked sessions cannot resume | First build | Viewer gets HTTP 410 after the session ends |
| Session | Endpoint can remove permissions mid-session | First build | Permissions can only be reduced, never added, during a session |
| Viewer | Native Windows viewer, monitor switching, full screen, fit, actual size | First build | Ctrl+Alt+F11 toggles full screen |
| Files | Two-pane file manager, multi-select, drag and drop from Explorer | First build | |
| Files | Recursive folders including empty ones, large files, progress, cancel, resume | First build | 256 KB chunks; resume verifies the partial file's SHA-256 |
| Files | Explicit Replace / Keep both / Skip, with "apply to all" | First build | Decided by the technician in the viewer |
| Files | Integrity check, permissions re-checked after reconnect | First build | SHA-256 per file; the server enforces transfer permission on every message |
| Files | Never auto-open or run received files | First build | Received files get the Mark of the Web |
| Clipboard | Unicode and multi-line text, RTF, HTML and images both ways | First build | Up to 7 MB per item |
| Clipboard | Sync toggle, direction control, Paste as keystrokes | First build | |
| Clipboard | Copied files go through the transfer queue | First build | Always asks first |
| Clipboard | Loop prevention, no stale pastes after reconnect, stops at session end, never logged | First build | Audit records counts only |
| Keyboard | Focused-viewer toggle sends shortcuts (Alt+Tab, Windows key) to the remote PC | First build | Low-level hook only while the viewer is focused |
| Keyboard | Toolbar keys: Ctrl+Alt+Del, Ctrl+Shift+Esc, Alt+Tab, Win, Win+R, Win+L, Alt+F4, Print Screen | First build | |
| Keyboard | Ctrl+Alt+Del through a supported mechanism | First build (limited) | Uses SendSAS, which needs a service component; the per-user agent reports that it is unavailable. See limitations |
| Keyboard | Held keys released on blur or disconnect; local escape Ctrl+Alt+F12 | First build | |
| Security | MFA (TOTP) for all console users, account lockout, rate limits | First build | |
| Security | Outbound-only authenticated TLS, certificate pinning | First build | The agent opens no listening ports |
| Security | One-time enrollment tokens and device revocation | First build | |
| Security | Independent view / control / clipboard / transfer permissions per user and client | First build | Enforced by the server relay |
| Security | Audit records (metadata only) | First build | Never screen, keystroke, clipboard or file contents |
| Updates | Signed, user-approved updates with rollback, never during a session | First build | Served by your own server; needs a signing key built in |
| Unattended | Unattended access | Later | Off by default when added; opt-in per device with visible indicator |
| Unattended | Windows service component (sign-in screen, UAC prompts, Ctrl+Alt+Del) | Later | Needed for SendSAS and secure-desktop capture |
| Viewer | Session recording | Later | Would need explicit consent and retention policy |
| Viewer | Chat, multiple technicians per session | Later | |
| Platform | macOS / Linux agents and viewers | Later | |
| Out of scope | Remote command shell, scripting, fleet-wide commands | Out of scope | Deliberately not built |
| Out of scope | Stealth or silent installs, hiding the session indicator | Out of scope | Deliberately not built |
| Out of scope | Public RDP, inbound agent ports, bypassing endpoint protection | Out of scope | Deliberately not built |
| Out of scope | Hosted service, server provisioning, public relay | Out of scope | The server is self-hosted by you |
