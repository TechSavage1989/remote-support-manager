# Remote desktop component evaluation

The task asked for a license-compatible remote desktop component. The requirement was a free solution (signing excepted), self-hostable, no inbound ports on endpoints, attended consent first, and source the owner can publish.

| Option | License | Fit | Decision |
|---|---|---|---|
| RustDesk (client and server) | AGPL-3.0 | Full remote desktop, but AGPL obligations for modified network use, separate Rust toolchain, its own ID/relay model and branding | Not used |
| Apache Guacamole | Apache-2.0 | Browser gateway to RDP/VNC/SSH. Needs RDP or VNC listening on the endpoint (inbound ports) and a Java server stack | Not used: conflicts with "no inbound ports" |
| MeshCentral | Apache-2.0 | Full device management including remote terminal and scripting. Large Node.js stack | Not used: brings remote shell and fleet scripting that are out of scope |
| TigerVNC / UltraVNC | GPL-2.0 | VNC server on the endpoint listens for inbound connections; GPL | Not used |
| FreeRDP | Apache-2.0 | RDP client library; needs RDP enabled and reachable on the endpoint | Not used: inbound RDP |
| **Built-in capture and input (this project)** | Project's own code | Windows GDI capture, SendInput, clipboard and Shell APIs called directly from Go; JPEG tiles over the authenticated WebSocket relay | **Chosen** |

Why the built-in approach:

- No license obligations beyond the Go modules listed in `go.mod` (BSD/MIT/ISC-style licenses: `coder/websocket` ISC, `modernc.org/sqlite` BSD-3-Clause, `golang.org/x/*` BSD-3-Clause, `rsc.io/qr` BSD-3-Clause).
- The agent only makes outbound TLS connections to your server. Nothing listens on the endpoint.
- The protocol is small enough to enforce permissions per message on the server.
- One language and toolchain for server, agent and viewer, cross-compiled to Windows.

Trade-offs to know about:

- GDI screen capture is simple and reliable but slower than DXGI Desktop Duplication on high-resolution screens. DXGI is a candidate for a later release.
- Without a Windows service component, the agent runs in the user's session: it cannot show the sign-in screen, UAC secure desktop or send Ctrl+Alt+Del. These are listed under "Later" in [FEATURE_MATRIX.md](FEATURE_MATRIX.md).
