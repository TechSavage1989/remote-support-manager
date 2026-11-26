package server

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// ClientConfig is written as client.json next to RemoteSupportAgent.exe in
// a download. The agent reads it on start.
type ClientConfig = protocol.ClientFile

func (s *Server) publicURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	return "https://" + r.Host
}

// handleCreateInvite creates a client invitation:
//   - onetime: portable client, plus a 6-digit code (any technician with view)
//   - support: persistent support session client (any technician with view)
//   - access: unattended access installer (administrators only)
//   - attended: attended agent (administrators only)
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		SiteID int64    `json:"siteId"`
		Kind   string   `json:"kind"`
		Label  string   `json:"label"`
		Perms  []string `json:"perms"`
		Hours  int      `json:"hours"`
	}
	if readJSON(r, &req) != nil || !protocol.ValidKind(req.Kind) {
		errJSON(w, 400, "bad request")
		return
	}
	cid, err := s.store.SiteClient(req.SiteID)
	if err != nil {
		errJSON(w, 404, "site not found")
		return
	}
	allowed, ok := p.permsFor(cid)
	if !ok {
		errJSON(w, 404, "site not found")
		return
	}
	if (req.Kind == protocol.KindAccess || req.Kind == protocol.KindAttended) && !p.user.IsAdmin {
		errJSON(w, 403, "only administrators can create installers for attended or unattended access")
		return
	}
	want := protocol.ParsePerms(req.Perms)
	want[protocol.PermView] = true
	perms := want.Intersect(allowed)
	if !perms.Has(protocol.PermView) {
		errJSON(w, 403, "you are not allowed to view devices of this client")
		return
	}
	ttl := map[string]time.Duration{protocol.KindOneTime: 30 * time.Minute, protocol.KindSupport: 24 * time.Hour, protocol.KindAccess: 72 * time.Hour, protocol.KindAttended: 24 * time.Hour}[req.Kind]
	if req.Hours > 0 && req.Hours <= 72 && req.Kind != protocol.KindOneTime {
		ttl = time.Duration(req.Hours) * time.Hour
	}
	if strings.TrimSpace(req.Label) == "" && req.Kind != protocol.KindAttended {
		errJSON(w, 400, "give the session a name so you can find it later")
		return
	}
	tok, code, inv, err := s.store.CreateInvite(req.SiteID, p.user.ID, req.Kind, req.Label, perms.List(), ttl)
	if err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "invite.created", ClientID: cid, IP: clientIP(r),
		Details: map[string]any{"invite": inv.ID, "kind": inv.Kind, "label": inv.Label, "perms": inv.Perms, "expiresAt": inv.ExpiresAt}})
	writeJSON(w, 200, map[string]any{"invite": inv, "token": tok, "code": code, "download": s.publicURL(r) + "/download/i/" + tok, "server": s.publicURL(r), "pin": s.cfg.CertPin})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request, p *principal) {
	list, err := s.store.ListInvites(p.clientIDs())
	if err != nil {
		errJSON(w, 500, "unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"invites": list})
}

func (s *Server) handleCancelInvite(w http.ResponseWriter, r *http.Request, p *principal) {
	inv, err := s.store.InviteByID(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "not found")
		return
	}
	if _, ok := p.permsFor(inv.ClientID); !ok || (inv.CreatedBy != p.user.ID && !p.user.IsAdmin) {
		errJSON(w, 404, "not found")
		return
	}
	if err := s.store.CancelInvite(inv.ID); err != nil {
		errJSON(w, 409, "already used or expired")
		return
	}
	s.store.Audit(AuditEntry{Actor: p.user.Email, Action: "invite.cancelled", ClientID: inv.ClientID, IP: clientIP(r), Details: map[string]any{"invite": inv.ID}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleInviteInfo lets a client show who is asking before the endpoint
// user agrees. Lookups by code are rate limited.
func (s *Server) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow("invite:"+ip, 10, time.Minute) {
		errJSON(w, 429, "too many attempts; wait a minute")
		return
	}
	q := r.URL.Query()
	inv, err := s.store.OpenInvite(strings.TrimSpace(q.Get("token")), strings.TrimSpace(q.Get("code")))
	if err != nil {
		s.store.Audit(AuditEntry{Actor: "endpoint@" + ip, Action: "invite.lookup_failed", IP: ip})
		errJSON(w, 404, ErrTokenInvalid.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"kind": inv.Kind, "label": inv.Label, "perms": inv.Perms, "technician": inv.TechName, "techEmail": inv.TechEmail, "org": s.cfg.OrgName, "expiresAt": inv.ExpiresAt})
}

func (s *Server) downloadFile(name string) ([]byte, error) {
	if s.cfg.DownloadsDir == "" {
		return nil, errors.New("downloads are not configured on this server (start it with --downloads)")
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.DownloadsDir, name))
	if err != nil {
		return nil, errors.New(name + " is not in the server's downloads folder")
	}
	return b, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) serveClientZip(w http.ResponseWriter, r *http.Request, cfg ClientConfig, name string) {
	exe, err := s.downloadFile("RemoteSupportAgent.exe")
	if err != nil {
		errJSON(w, 404, err.Error())
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("RemoteSupportAgent.exe")
	_, _ = f.Write(exe)
	j, _ := json.MarshalIndent(cfg, "", "  ")
	f, _ = zw.Create("client.json")
	_, _ = f.Write(j)
	f, _ = zw.Create("README.txt")
	_, _ = f.Write([]byte(readmeFor(cfg.Kind, cfg.Org)))
	if err := zw.Close(); err != nil {
		errJSON(w, 500, "could not build download")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+unsafeName.ReplaceAllString(name, "-")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func readmeFor(kind, org string) string {
	head := "Remote Support Manager - " + org + "\r\n\r\nExtract both files to the same folder, then run RemoteSupportAgent.exe.\r\n\r\n"
	switch kind {
	case protocol.KindOneTime:
		return head + "One-time support: nothing is installed. The program shows who is asking and what they may do; it closes and leaves nothing behind when the session ends.\r\n"
	case protocol.KindSupport:
		return head + "Support session: after you agree, the program installs for your Windows account and keeps the connection available until your technician closes the session. It then uninstalls itself. You can also remove it from Settings > Apps at any time.\r\n"
	case protocol.KindAccess:
		return head + "Unattended access: an administrator of this PC must approve the installation. Technicians of " + org + " can then connect at any time, also when nobody is signed in. A bar is shown on screen during every connection. Remove it from Settings > Apps at any time.\r\n"
	case protocol.KindAttended:
		return head + "Attended support agent: installs for your Windows account. Every support session needs your approval and the technician's code.\r\n"
	}
	return head + "If your technician gave you a 6-digit code, type it when asked.\r\n"
}

func (s *Server) handleDownloadInvite(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("download:"+clientIP(r), 30, time.Minute) {
		errJSON(w, 429, "too many attempts")
		return
	}
	tok := r.PathValue("token")
	inv, err := s.store.OpenInvite(tok, "")
	if err != nil {
		errJSON(w, 404, ErrTokenInvalid.Error())
		return
	}
	name := "RemoteSupport-" + inv.Kind
	if inv.Label != "" {
		name += "-" + inv.Label
	}
	s.serveClientZip(w, r, ClientConfig{Server: s.publicURL(r), Pin: s.cfg.CertPin, Org: s.cfg.OrgName, Invite: tok, Kind: inv.Kind}, name)
}

// handleDownloadClient serves the generic one-time client; the end user
// types the 6-digit code from their technician.
func (s *Server) handleDownloadClient(w http.ResponseWriter, r *http.Request) {
	s.serveClientZip(w, r, ClientConfig{Server: s.publicURL(r), Pin: s.cfg.CertPin, Org: s.cfg.OrgName, Kind: protocol.KindOneTime}, "RemoteSupport")
}

// handleDownloadViewer serves the viewer installer, or the portable viewer
// with ?portable=1 (also the fallback when no installer was published).
func (s *Server) handleDownloadViewer(w http.ResponseWriter, r *http.Request) {
	name := "RemoteSupportViewerSetup.exe"
	if r.URL.Query().Get("portable") == "1" {
		name = "RemoteSupportViewer.exe"
	}
	b, err := s.downloadFile(name)
	if err != nil {
		if b, err = s.downloadFile("RemoteSupportViewer.exe"); err != nil {
			errJSON(w, 404, err.Error())
			return
		}
		name = "RemoteSupportViewer.exe"
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(b)
}

// handleRemoveDevice deletes a device. The client is told to uninstall
// itself now, or the next time it connects (its credential stops working).
// Technicians may close one-time and support sessions they can see;
// removing attended agents or unattended access needs an administrator.
func (s *Server) handleRemoveDevice(w http.ResponseWriter, r *http.Request, p *principal) {
	d, _, ok := s.visibleDevice(w, r, p)
	if !ok {
		return
	}
	if (d.Kind == protocol.KindAccess || d.Kind == protocol.KindAttended) && !p.user.IsAdmin {
		errJSON(w, 403, "only administrators can remove this device")
		return
	}
	if err := s.removeDevice(d, "removed by "+p.user.Name, p.user.Email, clientIP(r)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errJSON(w, 409, "already removed")
			return
		}
		errJSON(w, 500, "could not remove")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) removeDevice(d *Device, reason, actor, ip string) error {
	if err := s.store.RevokeDevice(d.ID); err != nil {
		return err
	}
	s.hub.RemoveDevice(d.ID, reason)
	s.store.Audit(AuditEntry{Actor: actor, Action: "device.removed", ClientID: d.ClientID, DeviceID: d.ID, IP: ip, Details: map[string]any{"hostname": d.Hostname, "kind": d.Kind, "label": d.Label, "reason": reason}})
	return nil
}

func (s *Server) removeDeviceByID(id, reason string) {
	d, err := s.store.Device(id)
	if err != nil || d.Revoked {
		return
	}
	_ = s.removeDevice(d, reason, "system", "")
}
