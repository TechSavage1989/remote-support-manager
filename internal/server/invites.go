package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// migrate adds columns introduced after the first release. It is safe to
// run on every start.
func (s *Store) migrate() error {
	add := func(table, col, def string) error {
		rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil && n == col {
				return nil
			}
		}
		_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + def)
		return err
	}
	for _, c := range []struct{ t, c, d string }{
		{"devices", "kind", "TEXT NOT NULL DEFAULT 'attended'"},
		{"devices", "label", "TEXT NOT NULL DEFAULT ''"},
		{"devices", "standing", "TEXT NOT NULL DEFAULT '[]'"},
		{"enroll_tokens", "kind", "TEXT NOT NULL DEFAULT 'attended'"},
		{"enroll_tokens", "label", "TEXT NOT NULL DEFAULT ''"},
		{"enroll_tokens", "perms", "TEXT NOT NULL DEFAULT '[]'"},
		{"enroll_tokens", "code_hash", "TEXT NOT NULL DEFAULT ''"},
		{"enroll_tokens", "id", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := add(c.t, c.c, c.d); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`UPDATE enroll_tokens SET id='inv-'||lower(hex(randomblob(6))) WHERE id=''`)
	return err
}

// Invite is a one-time token that lets a client join: an attended agent,
// a one-time support client, a persistent support session or unattended
// access.
type Invite struct {
	ID        string   `json:"id"`
	SiteID    int64    `json:"siteId"`
	ClientID  int64    `json:"clientId"`
	SiteName  string   `json:"siteName"`
	Client    string   `json:"clientName"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Perms     []string `json:"perms"`
	CreatedBy int64    `json:"createdBy"`
	TechName  string   `json:"technician"`
	TechEmail string   `json:"techEmail"`
	ExpiresAt int64    `json:"expiresAt"`
	UsedAt    int64    `json:"usedAt"`
	DeviceID  string   `json:"deviceId"`
}

// CreateInvite stores a new invite. For one-time support it also returns a
// 6-digit code that the end user can type instead of using the download.
func (s *Store) CreateInvite(siteID, by int64, kind, label string, perms []string, ttl time.Duration) (token, code string, inv *Invite, err error) {
	if !protocol.ValidKind(kind) {
		return "", "", nil, errors.New("unknown client kind")
	}
	label = strings.TrimSpace(label)
	if len(label) > 120 {
		return "", "", nil, errors.New("label must be at most 120 characters")
	}
	token = "rsm-invite-" + RandomToken(20)
	if kind == protocol.KindAttended {
		token = "rsm-enroll-" + RandomToken(20)
	}
	codeHash := ""
	if kind == protocol.KindOneTime {
		for i := 0; i < 20; i++ {
			c := sixDigits()
			var n int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM enroll_tokens WHERE code_hash=? AND used_at=0 AND expires_at>?`, HashToken("code:"+c), now()).Scan(&n)
			if n == 0 {
				code, codeHash = c, HashToken("code:"+c)
				break
			}
		}
		if code == "" {
			return "", "", nil, errors.New("could not allocate a code; try again")
		}
	}
	id := "inv-" + RandomToken(6)
	p, _ := json.Marshal(protocol.ParsePerms(perms).List())
	exp := time.Now().Add(ttl).Unix()
	if _, err = s.db.Exec(`INSERT INTO enroll_tokens(token_hash,site_id,created_by,expires_at,kind,label,perms,code_hash,id) VALUES(?,?,?,?,?,?,?,?,?)`,
		HashToken(token), siteID, by, exp, kind, label, string(p), codeHash, id); err != nil {
		return "", "", nil, err
	}
	inv, err = s.inviteWhere(`t.id=?`, id)
	return token, code, inv, err
}

// CreateEnrollToken returns a one-time token for an attended agent.
func (s *Store) CreateEnrollToken(siteID, by int64, ttl time.Duration) (string, int64, error) {
	tok, _, inv, err := s.CreateInvite(siteID, by, protocol.KindAttended, "", nil, ttl)
	if err != nil {
		return "", 0, err
	}
	return tok, inv.ExpiresAt, nil
}

const inviteCols = `t.id,t.site_id,s.client_id,s.name,c.name,t.kind,t.label,t.perms,t.created_by,COALESCE(u.name,''),COALESCE(u.email,''),t.expires_at,t.used_at,t.device_id`
const inviteFrom = ` FROM enroll_tokens t JOIN sites s ON s.id=t.site_id JOIN clients c ON c.id=s.client_id LEFT JOIN users u ON u.id=t.created_by `

func scanInvite(r interface{ Scan(...any) error }) (*Invite, error) {
	inv := &Invite{}
	var perms string
	if err := r.Scan(&inv.ID, &inv.SiteID, &inv.ClientID, &inv.SiteName, &inv.Client, &inv.Kind, &inv.Label, &perms, &inv.CreatedBy, &inv.TechName, &inv.TechEmail, &inv.ExpiresAt, &inv.UsedAt, &inv.DeviceID); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(perms), &inv.Perms)
	if inv.Perms == nil {
		inv.Perms = []string{}
	}
	return inv, nil
}

func (s *Store) inviteWhere(where string, args ...any) (*Invite, error) {
	return scanInvite(s.db.QueryRow(`SELECT `+inviteCols+inviteFrom+`WHERE `+where, args...))
}

// ErrTokenInvalid is returned for unknown, used or expired tokens.
var ErrTokenInvalid = errors.New("this invitation is invalid, expired or already used")

// OpenInvite finds an unused, unexpired invite by token or one-time code.
func (s *Store) OpenInvite(token, code string) (*Invite, error) {
	var inv *Invite
	var err error
	switch {
	case token != "":
		inv, err = s.inviteWhere(`t.token_hash=?`, HashToken(token))
	case len(code) == 6:
		inv, err = s.inviteWhere(`t.code_hash=? AND t.used_at=0 AND t.expires_at>?`, HashToken("code:"+code), now())
	default:
		return nil, ErrTokenInvalid
	}
	if err != nil || inv.UsedAt != 0 || inv.ExpiresAt < now() {
		return nil, ErrTokenInvalid
	}
	return inv, nil
}

// ListInvites returns open invites for the given clients (nil = all).
func (s *Store) ListInvites(clientIDs []int64) ([]*Invite, error) {
	rows, err := s.db.Query(`SELECT `+inviteCols+inviteFrom+`WHERE t.used_at=0 AND t.expires_at>? AND t.id!='' ORDER BY t.expires_at`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := map[int64]bool{}
	for _, c := range clientIDs {
		allowed[c] = true
	}
	out := []*Invite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		if clientIDs == nil || allowed[inv.ClientID] {
			out = append(out, inv)
		}
	}
	return out, rows.Err()
}

// InviteByID returns an invite (used or not).
func (s *Store) InviteByID(id string) (*Invite, error) { return s.inviteWhere(`t.id=?`, id) }

// CancelInvite makes an unused invite unusable.
func (s *Store) CancelInvite(id string) error {
	r, err := s.db.Exec(`UPDATE enroll_tokens SET expires_at=0 WHERE id=? AND used_at=0`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Enroll consumes an invite atomically and creates the device. granted is
// what the endpoint user allowed in the client's consent window; for
// clients with standing consent it is stored and caps every later session.
func (s *Store) Enroll(token, code, hostname string, granted []string) (deviceID, secret string, inv *Invite, err error) {
	inv, err = s.OpenInvite(token, code)
	if err != nil {
		return "", "", nil, err
	}
	standing := []string{}
	switch inv.Kind {
	case protocol.KindOneTime, protocol.KindSupport:
		standing = protocol.ParsePerms(inv.Perms).Intersect(protocol.ParsePerms(granted)).List()
		if !protocol.ParsePerms(standing).Has(protocol.PermView) {
			return "", "", nil, errors.New("the screen must be shared for remote support")
		}
	case protocol.KindAccess:
		standing = protocol.ParsePerms(inv.Perms).List()
	}
	st, _ := json.Marshal(standing)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", nil, err
	}
	defer tx.Rollback()
	deviceID = "dev-" + RandomToken(8)
	secret = RandomToken(32)
	r, err := tx.Exec(`UPDATE enroll_tokens SET used_at=?, device_id=? WHERE id=? AND used_at=0 AND expires_at>=?`, now(), deviceID, inv.ID, now())
	if err != nil {
		return "", "", nil, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return "", "", nil, ErrTokenInvalid
	}
	if _, err := tx.Exec(`INSERT INTO devices(id,site_id,secret_hash,hostname,enrolled_at,last_seen,kind,label,standing) VALUES(?,?,?,?,?,?,?,?,?)`,
		deviceID, inv.SiteID, HashToken(secret), hostname, now(), now(), inv.Kind, inv.Label, string(st)); err != nil {
		return "", "", nil, err
	}
	inv.DeviceID = deviceID
	return deviceID, secret, inv, tx.Commit()
}
