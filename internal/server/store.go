package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  name TEXT NOT NULL,
  pass_hash TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0,
  totp_secret TEXT NOT NULL DEFAULT '',
  totp_last_step INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until INTEGER NOT NULL DEFAULT 0,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS clients (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sites (
  id INTEGER PRIMARY KEY,
  client_id INTEGER NOT NULL REFERENCES clients(id),
  name TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(client_id, name)
);
CREATE TABLE IF NOT EXISTS assignments (
  user_id INTEGER NOT NULL REFERENCES users(id),
  client_id INTEGER NOT NULL REFERENCES clients(id),
  perms TEXT NOT NULL,
  PRIMARY KEY(user_id, client_id)
);
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  site_id INTEGER NOT NULL REFERENCES sites(id),
  secret_hash TEXT NOT NULL,
  hostname TEXT NOT NULL DEFAULT '',
  os_name TEXT NOT NULL DEFAULT '',
  os_version TEXT NOT NULL DEFAULT '',
  uptime_sec INTEGER NOT NULL DEFAULT 0,
  disks TEXT NOT NULL DEFAULT '[]',
  agent_version TEXT NOT NULL DEFAULT '',
  logged_on_user TEXT NOT NULL DEFAULT '',
  last_seen INTEGER NOT NULL DEFAULT 0,
  enrolled_at INTEGER NOT NULL,
  revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash TEXT PRIMARY KEY,
  site_id INTEGER NOT NULL REFERENCES sites(id),
  created_by INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER NOT NULL DEFAULT 0,
  device_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS web_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  csrf TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY,
  at INTEGER NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  client_id INTEGER NOT NULL DEFAULT 0,
  device_id TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT '',
  details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS audit_at ON audit(at);
CREATE TABLE IF NOT EXISTS support_sessions (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES devices(id),
  user_id INTEGER NOT NULL REFERENCES users(id),
  requested TEXT NOT NULL,
  granted TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  started_at INTEGER NOT NULL DEFAULT 0,
  ended_at INTEGER NOT NULL DEFAULT 0,
  end_reason TEXT NOT NULL DEFAULT ''
);
`

// OpenStore opens (and migrates) the database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func now() int64 { return time.Now().Unix() }

// RandomToken returns n random bytes, hex encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// HashToken hashes a bearer secret for storage.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// NewTOTPSecret returns a base32 secret.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// ---------------------------------------------------------------- users

type User struct {
	ID                 int64  `json:"id"`
	Email              string `json:"email"`
	Name               string `json:"name"`
	IsAdmin            bool   `json:"isAdmin"`
	MFAEnrolled        bool   `json:"mfaEnrolled"`
	Disabled           bool   `json:"disabled"`
	MustChangePassword bool   `json:"mustChangePassword"`
	passHash           string
	totpSecret         string
	totpLastStep       int64
	failed             int
	lockedUntil        int64
}

const userCols = `id,email,name,pass_hash,is_admin,totp_secret,totp_last_step,disabled,failed_logins,locked_until,must_change_password`

func scanUser(r interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var admin, disabled, must int
	if err := r.Scan(&u.ID, &u.Email, &u.Name, &u.passHash, &admin, &u.totpSecret, &u.totpLastStep, &disabled, &u.failed, &u.lockedUntil, &must); err != nil {
		return nil, err
	}
	u.IsAdmin, u.Disabled, u.MustChangePassword = admin == 1, disabled == 1, must == 1
	u.MFAEnrolled = u.totpSecret != "" && !strings.HasPrefix(u.totpSecret, "pending:")
	return u, nil
}

func (s *Store) CreateUser(email, name, passHash string, admin, mustChange bool) (int64, error) {
	r, err := s.db.Exec(`INSERT INTO users(email,name,pass_hash,is_admin,must_change_password,created_at) VALUES(?,?,?,?,?,?)`,
		strings.TrimSpace(email), strings.TrimSpace(name), passHash, b2i(admin), b2i(mustChange), now())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) UserByEmail(email string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email=?`, strings.TrimSpace(email)))
}

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) SetLoginFailure(id int64, failed int, lockedUntil int64) error {
	_, err := s.db.Exec(`UPDATE users SET failed_logins=?, locked_until=? WHERE id=?`, failed, lockedUntil, id)
	return err
}

func (s *Store) SetTOTP(id int64, secret string) error {
	_, err := s.db.Exec(`UPDATE users SET totp_secret=?, totp_last_step=0 WHERE id=?`, secret, id)
	return err
}

// UseTOTPStep records the last accepted step; it fails if step was already
// used, preventing code replay.
func (s *Store) UseTOTPStep(id, step int64) (bool, error) {
	r, err := s.db.Exec(`UPDATE users SET totp_last_step=? WHERE id=? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

func (s *Store) SetPassword(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET pass_hash=?, must_change_password=0 WHERE id=?`, hash, id)
	return err
}

func (s *Store) SetDisabled(id int64, disabled bool) error {
	_, err := s.db.Exec(`UPDATE users SET disabled=? WHERE id=?`, b2i(disabled), id)
	if err == nil && disabled {
		_, err = s.db.Exec(`DELETE FROM web_sessions WHERE user_id=?`, id)
	}
	return err
}

// ---------------------------------------------------------------- web sessions

func (s *Store) CreateWebSession(userID int64, ttl time.Duration) (token, csrf string, err error) {
	token, csrf = RandomToken(32), RandomToken(16)
	_, err = s.db.Exec(`INSERT INTO web_sessions(token_hash,user_id,csrf,expires_at) VALUES(?,?,?,?)`, HashToken(token), userID, csrf, time.Now().Add(ttl).Unix())
	return
}

func (s *Store) WebSession(token string) (userID int64, csrf string, err error) {
	var exp int64
	err = s.db.QueryRow(`SELECT user_id,csrf,expires_at FROM web_sessions WHERE token_hash=?`, HashToken(token)).Scan(&userID, &csrf, &exp)
	if err == nil && exp < now() {
		err = sql.ErrNoRows
	}
	return
}

func (s *Store) DeleteWebSession(token string) {
	_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE token_hash=?`, HashToken(token))
}

// ---------------------------------------------------------------- clients and sites

type Client struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Sites []*Site `json:"sites"`
}

type Site struct {
	ID       int64     `json:"id"`
	ClientID int64     `json:"clientId"`
	Name     string    `json:"name"`
	Devices  []*Device `json:"devices"`
}

type Disk struct {
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

type Device struct {
	ID           string `json:"id"`
	SiteID       int64  `json:"siteId"`
	ClientID     int64  `json:"clientId"`
	SiteName     string `json:"siteName"`
	ClientName   string `json:"clientName"`
	Hostname     string `json:"hostname"`
	OSName       string `json:"osName"`
	OSVersion    string `json:"osVersion"`
	UptimeSec    int64  `json:"uptimeSec"`
	Disks        []Disk `json:"disks"`
	AgentVersion string `json:"agentVersion"`
	LoggedOnUser string `json:"loggedOnUser"`
	LastSeen     int64  `json:"lastSeen"`
	EnrolledAt   int64  `json:"enrolledAt"`
	Revoked      bool   `json:"revoked"`
	Online       bool   `json:"online"`
	secretHash   string
}

func (s *Store) CreateClient(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return 0, errors.New("client name must be 1-120 characters")
	}
	r, err := s.db.Exec(`INSERT INTO clients(name,created_at) VALUES(?,?)`, name, now())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) CreateSite(clientID int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return 0, errors.New("site name must be 1-120 characters")
	}
	r, err := s.db.Exec(`INSERT INTO sites(client_id,name,created_at) VALUES(?,?,?)`, clientID, name, now())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) SiteClient(siteID int64) (int64, error) {
	var c int64
	err := s.db.QueryRow(`SELECT client_id FROM sites WHERE id=?`, siteID).Scan(&c)
	return c, err
}

func (s *Store) ClientExists(id int64) bool {
	var x int64
	return s.db.QueryRow(`SELECT id FROM clients WHERE id=?`, id).Scan(&x) == nil
}

const deviceCols = `d.id,d.site_id,s.client_id,s.name,c.name,d.hostname,d.os_name,d.os_version,d.uptime_sec,d.disks,d.agent_version,d.logged_on_user,d.last_seen,d.enrolled_at,d.revoked_at,d.secret_hash`
const deviceFrom = ` FROM devices d JOIN sites s ON s.id=d.site_id JOIN clients c ON c.id=s.client_id `

func scanDevice(r interface{ Scan(...any) error }) (*Device, error) {
	d := &Device{}
	var disks string
	var revoked int64
	if err := r.Scan(&d.ID, &d.SiteID, &d.ClientID, &d.SiteName, &d.ClientName, &d.Hostname, &d.OSName, &d.OSVersion, &d.UptimeSec, &disks, &d.AgentVersion, &d.LoggedOnUser, &d.LastSeen, &d.EnrolledAt, &revoked, &d.secretHash); err != nil {
		return nil, err
	}
	d.Revoked = revoked != 0
	_ = json.Unmarshal([]byte(disks), &d.Disks)
	if d.Disks == nil {
		d.Disks = []Disk{}
	}
	return d, nil
}

func (s *Store) Device(id string) (*Device, error) {
	return scanDevice(s.db.QueryRow(`SELECT `+deviceCols+deviceFrom+`WHERE d.id=?`, id))
}

// Tree returns clients, sites and devices. clientIDs limits the result; nil
// means all clients (administrators).
func (s *Store) Tree(clientIDs []int64, query string) ([]*Client, error) {
	allowed := map[int64]bool{}
	for _, id := range clientIDs {
		allowed[id] = true
	}
	all := clientIDs == nil
	rows, err := s.db.Query(`SELECT id,name FROM clients ORDER BY name`)
	if err != nil {
		return nil, err
	}
	clients := []*Client{}
	byID := map[int64]*Client{}
	for rows.Next() {
		c := &Client{Sites: []*Site{}}
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			rows.Close()
			return nil, err
		}
		if all || allowed[c.ID] {
			clients = append(clients, c)
			byID[c.ID] = c
		}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT id,client_id,name FROM sites ORDER BY name`)
	if err != nil {
		return nil, err
	}
	sites := map[int64]*Site{}
	for rows.Next() {
		st := &Site{Devices: []*Device{}}
		if err := rows.Scan(&st.ID, &st.ClientID, &st.Name); err != nil {
			rows.Close()
			return nil, err
		}
		if c := byID[st.ClientID]; c != nil {
			c.Sites = append(c.Sites, st)
			sites[st.ID] = st
		}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT ` + deviceCols + deviceFrom + `WHERE d.revoked_at=0 ORDER BY d.hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		if st := sites[d.SiteID]; st != nil {
			st.Devices = append(st.Devices, d)
		}
	}
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		filtered := []*Client{}
		for _, c := range clients {
			cm := strings.Contains(strings.ToLower(c.Name), q)
			nc := &Client{ID: c.ID, Name: c.Name, Sites: []*Site{}}
			for _, st := range c.Sites {
				sm := cm || strings.Contains(strings.ToLower(st.Name), q)
				ns := &Site{ID: st.ID, ClientID: st.ClientID, Name: st.Name, Devices: []*Device{}}
				for _, d := range st.Devices {
					if sm || deviceMatches(d, q) {
						ns.Devices = append(ns.Devices, d)
					}
				}
				if sm || len(ns.Devices) > 0 {
					nc.Sites = append(nc.Sites, ns)
				}
			}
			if cm || len(nc.Sites) > 0 {
				filtered = append(filtered, nc)
			}
		}
		clients = filtered
	}
	return clients, rows.Err()
}

func deviceMatches(d *Device, q string) bool {
	for _, f := range []string{d.Hostname, d.OSName, d.OSVersion, d.LoggedOnUser, d.AgentVersion, d.ID} {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- assignments

func (s *Store) SetAssignment(userID, clientID int64, perms []string) error {
	if len(perms) == 0 {
		_, err := s.db.Exec(`DELETE FROM assignments WHERE user_id=? AND client_id=?`, userID, clientID)
		return err
	}
	p, _ := json.Marshal(perms)
	_, err := s.db.Exec(`INSERT INTO assignments(user_id,client_id,perms) VALUES(?,?,?) ON CONFLICT(user_id,client_id) DO UPDATE SET perms=excluded.perms`, userID, clientID, string(p))
	return err
}

// Assignments returns client id -> allowed permissions for a user.
func (s *Store) Assignments(userID int64) (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT client_id,perms FROM assignments WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var c int64
		var p string
		if err := rows.Scan(&c, &p); err != nil {
			return nil, err
		}
		var list []string
		_ = json.Unmarshal([]byte(p), &list)
		out[c] = list
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- enrollment

// CreateEnrollToken returns a one-time token for a site.
func (s *Store) CreateEnrollToken(siteID, by int64, ttl time.Duration) (string, int64, error) {
	tok := "rsm-enroll-" + RandomToken(20)
	exp := time.Now().Add(ttl).Unix()
	_, err := s.db.Exec(`INSERT INTO enroll_tokens(token_hash,site_id,created_by,expires_at) VALUES(?,?,?,?)`, HashToken(tok), siteID, by, exp)
	return tok, exp, err
}

// ErrTokenInvalid is returned for unknown, used or expired tokens.
var ErrTokenInvalid = errors.New("enrollment token is invalid, expired or already used")

// Enroll consumes a token atomically and creates the device.
func (s *Store) Enroll(token, hostname string) (deviceID, secret string, siteID int64, err error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", 0, err
	}
	defer tx.Rollback()
	th := HashToken(token)
	var exp, used int64
	if err := tx.QueryRow(`SELECT site_id,expires_at,used_at FROM enroll_tokens WHERE token_hash=?`, th).Scan(&siteID, &exp, &used); err != nil {
		return "", "", 0, ErrTokenInvalid
	}
	if used != 0 || exp < now() {
		return "", "", 0, ErrTokenInvalid
	}
	deviceID = "dev-" + RandomToken(8)
	secret = RandomToken(32)
	r, err := tx.Exec(`UPDATE enroll_tokens SET used_at=?, device_id=? WHERE token_hash=? AND used_at=0`, now(), deviceID, th)
	if err != nil {
		return "", "", 0, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return "", "", 0, ErrTokenInvalid
	}
	if _, err := tx.Exec(`INSERT INTO devices(id,site_id,secret_hash,hostname,enrolled_at,last_seen) VALUES(?,?,?,?,?,?)`, deviceID, siteID, HashToken(secret), hostname, now(), now()); err != nil {
		return "", "", 0, err
	}
	return deviceID, secret, siteID, tx.Commit()
}

// AuthDevice checks a device credential. Revoked devices fail.
func (s *Store) AuthDevice(id, secret string) (*Device, error) {
	d, err := s.Device(id)
	if err != nil || d.Revoked {
		return nil, errors.New("device not authorised")
	}
	if !ConstantEq(d.secretHash, HashToken(secret)) {
		return nil, errors.New("device not authorised")
	}
	return d, nil
}

func (s *Store) RevokeDevice(id string) error {
	r, err := s.db.Exec(`UPDATE devices SET revoked_at=?, secret_hash='revoked' WHERE id=? AND revoked_at=0`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Inventory is what the agent reports.
type Inventory struct {
	Hostname     string `json:"hostname"`
	OSName       string `json:"osName"`
	OSVersion    string `json:"osVersion"`
	UptimeSec    int64  `json:"uptimeSec"`
	Disks        []Disk `json:"disks"`
	AgentVersion string `json:"agentVersion"`
	LoggedOnUser string `json:"loggedOnUser"`
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Store) UpdateInventory(id string, inv Inventory) error {
	if len(inv.Disks) > 32 {
		inv.Disks = inv.Disks[:32]
	}
	disks, _ := json.Marshal(inv.Disks)
	_, err := s.db.Exec(`UPDATE devices SET hostname=?,os_name=?,os_version=?,uptime_sec=?,disks=?,agent_version=?,logged_on_user=?,last_seen=? WHERE id=?`,
		clip(inv.Hostname, 255), clip(inv.OSName, 255), clip(inv.OSVersion, 255), inv.UptimeSec, string(disks), clip(inv.AgentVersion, 64), clip(inv.LoggedOnUser, 255), now(), id)
	return err
}

func (s *Store) Touch(id string) {
	_, _ = s.db.Exec(`UPDATE devices SET last_seen=? WHERE id=?`, now(), id)
}

// ---------------------------------------------------------------- support sessions

func (s *Store) CreateSupportSession(id, deviceID string, userID int64, requested []string) error {
	p, _ := json.Marshal(requested)
	_, err := s.db.Exec(`INSERT INTO support_sessions(id,device_id,user_id,requested,state,created_at) VALUES(?,?,?,?,?,?)`, id, deviceID, userID, string(p), "pending", now())
	return err
}

func (s *Store) MarkSessionActive(id string, granted []string) error {
	p, _ := json.Marshal(granted)
	_, err := s.db.Exec(`UPDATE support_sessions SET state='active', granted=?, started_at=? WHERE id=?`, string(p), now(), id)
	return err
}

func (s *Store) MarkSessionEnded(id, reason string) {
	_, _ = s.db.Exec(`UPDATE support_sessions SET state='ended', ended_at=?, end_reason=? WHERE id=? AND state!='ended'`, now(), reason, id)
}

// ---------------------------------------------------------------- audit

type AuditEntry struct {
	ID        int64          `json:"id"`
	At        int64          `json:"at"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	ClientID  int64          `json:"clientId"`
	DeviceID  string         `json:"deviceId"`
	SessionID string         `json:"sessionId"`
	IP        string         `json:"ip"`
	Details   map[string]any `json:"details"`
}

func (s *Store) Audit(e AuditEntry) {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	d, _ := json.Marshal(e.Details)
	_, _ = s.db.Exec(`INSERT INTO audit(at,actor,action,client_id,device_id,session_id,ip,details) VALUES(?,?,?,?,?,?,?,?)`,
		now(), e.Actor, e.Action, e.ClientID, e.DeviceID, e.SessionID, e.IP, string(d))
}

// AuditList returns recent entries, optionally limited to some clients.
func (s *Store) AuditList(clientIDs []int64, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id,at,actor,action,client_id,device_id,session_id,ip,details FROM audit ORDER BY id DESC LIMIT ?`, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := map[int64]bool{}
	for _, c := range clientIDs {
		allowed[c] = true
	}
	out := []AuditEntry{}
	for rows.Next() && len(out) < limit {
		var e AuditEntry
		var d string
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.ClientID, &e.DeviceID, &e.SessionID, &e.IP, &d); err != nil {
			return nil, err
		}
		if clientIDs != nil && !allowed[e.ClientID] {
			continue
		}
		_ = json.Unmarshal([]byte(d), &e.Details)
		out = append(out, e)
	}
	return out, rows.Err()
}
