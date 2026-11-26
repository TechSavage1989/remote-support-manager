// Package update implements the user-approved updater shared by the viewer
// and the agent.
//
// Rules this package enforces (see AGENTS.md, "Updates"):
//   - Checking is the only automatic step. Downloading and installing each
//     need an explicit user action in the app.
//   - The manifest is signed with Ed25519. The public key is compiled into
//     the app; an unsigned or wrongly signed manifest is rejected and nothing
//     is downloaded.
//   - The downloaded file must match the manifest's size and SHA-256 and be a
//     Windows executable before it is staged.
//   - Installing keeps exactly one backup of the running version and rolls
//     back if the new version does not confirm a healthy start.
//   - Callers must not install while a support session is active; Install
//     takes a guard function and refuses when it reports busy.
package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PublicKey is the base64 Ed25519 key that update manifests must be signed
// with. Release builds set it with
//
//	-ldflags "-X github.com/TechSavage1989/remote-support-manager/internal/update.PublicKey=<key>"
//
// When it is empty the apps say updates are turned off and never download.
var PublicKey = ""

// Manifest describes one published build of one product.
type Manifest struct {
	Product   string `json:"product"` // "viewer" or "agent"
	Version   string `json:"version"`
	File      string `json:"file"` // file name next to the manifest
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Notes     string `json:"notes"`
	Published string `json:"published"`
	// MinVersion, when set, means versions below it cannot update directly.
	MinVersion string `json:"minVersion,omitempty"`
}

// Errors callers show to the user.
var (
	ErrNoKey       = errors.New("updates are turned off in this build (no update signing key was compiled in)")
	ErrBadSig      = errors.New("the update information is not signed by the publisher, so it was ignored")
	ErrBusy        = errors.New("a support session is active; install the update after the session ends")
	ErrMismatch    = errors.New("the downloaded file does not match the signed update information and was deleted")
	ErrNotExe      = errors.New("the downloaded file is not a Windows program and was deleted")
	ErrWrongProd   = errors.New("the update is for a different program")
	ErrInProgress  = errors.New("an update check or download is already running")
	ErrNoFeed      = errors.New("no update source is configured")
	ErrBackoff     = errors.New("the last check failed; waiting before trying again")
	ErrNotModified = errors.New("no change since the last check")
)

// SignManifest returns the detached signature (base64) for manifest bytes.
func SignManifest(priv ed25519.PrivateKey, manifest []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
}

// VerifyManifest checks the signature and parses the manifest.
func VerifyManifest(pubB64 string, manifest []byte, sigB64 string) (*Manifest, error) {
	if strings.TrimSpace(pubB64) == "" {
		return nil, ErrNoKey
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, ErrNoKey
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), manifest, sig) {
		return nil, ErrBadSig
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("update information is unreadable: %w", err)
	}
	if m.Version == "" || m.File == "" || m.Size <= 0 || len(m.SHA256) != 64 || strings.ContainsAny(m.File, `/\:`) {
		return nil, errors.New("update information is incomplete")
	}
	return &m, nil
}

// Compare compares dotted versions with an optional "-suffix" (a suffix
// sorts before the same version without one). It returns -1, 0 or 1.
func Compare(a, b string) int {
	split := func(v string) ([]int, string) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		pre := ""
		if i := strings.IndexAny(v, "-+"); i >= 0 {
			v, pre = v[:i], v[i+1:]
		}
		var out []int
		for _, p := range strings.Split(v, ".") {
			n, _ := strconv.Atoi(p)
			out = append(out, n)
		}
		return out, pre
	}
	av, ap := split(a)
	bv, bp := split(b)
	for len(av) < len(bv) {
		av = append(av, 0)
	}
	for len(bv) < len(av) {
		bv = append(bv, 0)
	}
	for i := range av {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	case ap < bp:
		return -1
	}
	return 1
}

// Checker performs at most one check or download at a time, remembers the
// ETag of the last manifest and backs off after failures.
type Checker struct {
	Product   string
	Current   string
	PublicKey string // base64 Ed25519
	FeedURL   string // https://server/updates/<product>-manifest.json
	TLS       *tls.Config

	mu      sync.Mutex
	busy    bool
	etag    string
	last    *Manifest
	fails   int
	nextTry time.Time
	now     func() time.Time
}

func (c *Checker) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Checker) client(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.TLS != nil {
		tr.TLSClientConfig = c.TLS
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func (c *Checker) begin(manual bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy {
		return ErrInProgress
	}
	if !manual && c.clock().Before(c.nextTry) {
		return ErrBackoff
	}
	c.busy = true
	return nil
}

func (c *Checker) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	if err == nil || errors.Is(err, ErrNotModified) {
		c.fails = 0
		c.nextTry = time.Time{}
		return
	}
	c.fails++
	d := time.Minute << min(c.fails, 8) // 2 min … ~4 h
	c.nextTry = c.clock().Add(d)
}

// Busy reports whether a check or download is running.
func (c *Checker) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy
}

// Check fetches and verifies the manifest. It returns the manifest when a
// newer version is available, nil when up to date. manual=true skips the
// failure backoff (the user pressed "Check for updates").
func (c *Checker) Check(ctx context.Context, manual bool) (m *Manifest, err error) {
	if c.PublicKey == "" {
		return nil, ErrNoKey
	}
	if c.FeedURL == "" {
		return nil, ErrNoFeed
	}
	if err := c.begin(manual); err != nil {
		return nil, err
	}
	defer func() { c.end(err) }()
	hc := c.client(30 * time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.FeedURL, nil)
	c.mu.Lock()
	if c.etag != "" && c.last != nil {
		req.Header.Set("If-None-Match", c.etag)
	}
	c.mu.Unlock()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the update source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		c.mu.Lock()
		last := c.last
		c.mu.Unlock()
		if last != nil && Compare(last.Version, c.Current) > 0 {
			return last, nil
		}
		return nil, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // nothing published
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update source answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	sreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.FeedURL+".sig", nil)
	sresp, err := hc.Do(sreq)
	if err != nil {
		return nil, fmt.Errorf("could not reach the update source: %w", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		return nil, ErrBadSig
	}
	sig, _ := io.ReadAll(io.LimitReader(sresp.Body, 4<<10))
	m, err = VerifyManifest(c.PublicKey, body, string(sig))
	if err != nil {
		return nil, err
	}
	if m.Product != c.Product {
		return nil, ErrWrongProd
	}
	c.mu.Lock()
	c.etag = resp.Header.Get("ETag")
	c.last = m
	c.mu.Unlock()
	if Compare(m.Version, c.Current) <= 0 {
		return nil, nil
	}
	if m.MinVersion != "" && Compare(c.Current, m.MinVersion) < 0 {
		return nil, fmt.Errorf("version %s cannot update directly to %s; install %s or later first", c.Current, m.Version, m.MinVersion)
	}
	return m, nil
}

// Download fetches the file named in m into dir, verifying size, SHA-256 and
// the executable header. It resumes a partial download in dir when present.
// The returned path is the staged file.
func (c *Checker) Download(ctx context.Context, m *Manifest, dir string, progress func(done, total int64)) (path string, err error) {
	if err := c.begin(true); err != nil {
		return "", err
	}
	defer func() { c.end(err) }()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(dir, m.File)
	if ok, _ := fileMatches(final, m); ok {
		return final, nil
	}
	part := final + ".part"
	var have int64
	if st, err := os.Stat(part); err == nil && st.Size() < m.Size {
		have = st.Size()
	} else {
		_ = os.Remove(part)
	}
	u := c.FeedURL[:strings.LastIndex(c.FeedURL, "/")+1] + m.File
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := c.client(0).Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		have = 0
		flags |= os.O_TRUNC
	default:
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 256<<10)
	done := have
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if done+int64(n) > m.Size {
				f.Close()
				os.Remove(part)
				return "", ErrMismatch
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return "", werr
			}
			done += int64(n)
			if progress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				progress(done, m.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return "", fmt.Errorf("download interrupted (it will resume next time): %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if progress != nil {
		progress(done, m.Size)
	}
	ok, err := fileMatches(part, m)
	if err != nil || !ok {
		os.Remove(part)
		if errors.Is(err, ErrNotExe) {
			return "", ErrNotExe
		}
		return "", ErrMismatch
	}
	_ = os.Remove(final)
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return final, nil
}

func fileMatches(path string, m *Manifest) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() != m.Size {
		return false, err
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("MZ")) {
		return false, ErrNotExe
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256), nil
}

// ------------------------------------------------------------ install

// Pending records an installed-but-unconfirmed update next to the program.
type Pending struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Backup   string `json:"backup"`
	Attempts int    `json:"attempts"`
}

func pendingPath(exe string) string { return exe + ".update.json" }

// BackupPath is the single rollback copy kept for exe.
func BackupPath(exe string) string { return exe + ".previous" }

// Install replaces exe with staged, keeping one backup, after checking the
// busy guard. The caller then restarts the program (with the user's
// approval, which they gave by pressing "Install and restart").
func Install(exe, staged, fromVersion, toVersion string, busy func() bool) error {
	if busy != nil && busy() {
		return ErrBusy
	}
	backup := BackupPath(exe)
	_ = os.Remove(backup)
	// Windows allows renaming a running executable but not overwriting it.
	if err := os.Rename(exe, backup); err != nil {
		return fmt.Errorf("could not set aside the current version: %w", err)
	}
	if err := copyFile(staged, exe); err != nil {
		_ = os.Remove(exe)
		if rerr := os.Rename(backup, exe); rerr != nil {
			return fmt.Errorf("install failed (%v) and restoring the previous version also failed: %w", err, rerr)
		}
		return fmt.Errorf("install failed, previous version kept: %w", err)
	}
	p := Pending{From: fromVersion, To: toVersion, Backup: backup}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(pendingPath(exe), b, 0o600); err != nil {
		return err
	}
	_ = os.Remove(staged)
	return nil
}

// StartupCheck runs early at every start. If an update is pending and this
// is the new version, it counts the attempt; after two unconfirmed starts it
// restores the backup and returns rolledBack=true (the caller should then
// exit and start the restored program). A running old version (for example
// after a manual rollback) clears the record.
func StartupCheck(exe, running string) (rolledBack bool, err error) {
	b, err := os.ReadFile(pendingPath(exe))
	if err != nil {
		return false, nil
	}
	var p Pending
	if json.Unmarshal(b, &p) != nil {
		_ = os.Remove(pendingPath(exe))
		return false, nil
	}
	if Compare(running, p.To) != 0 {
		_ = os.Remove(pendingPath(exe))
		return false, nil
	}
	p.Attempts++
	if p.Attempts > 2 {
		return true, Rollback(exe)
	}
	b, _ = json.Marshal(p)
	return false, os.WriteFile(pendingPath(exe), b, 0o600)
}

// ConfirmHealthy is called once the new version has started successfully
// (main window created and settings loaded). The backup is kept until the
// next update so the user can still roll back by hand.
func ConfirmHealthy(exe string) { _ = os.Remove(pendingPath(exe)) }

// Rollback restores the backup copy.
func Rollback(exe string) error {
	backup := BackupPath(exe)
	if _, err := os.Stat(backup); err != nil {
		return errors.New("there is no previous version to restore")
	}
	broken := exe + ".failed"
	_ = os.Remove(broken)
	if err := os.Rename(exe, broken); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(backup, exe); err != nil {
		_ = os.Rename(broken, exe)
		return err
	}
	_ = os.Remove(pendingPath(exe))
	return nil
}

// Cleanup removes leftovers from earlier updates (the failed copy).
func Cleanup(exe string) { _ = os.Remove(exe + ".failed") }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
