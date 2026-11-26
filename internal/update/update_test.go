package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type feed struct {
	dir  string
	priv ed25519.PrivateKey
	pub  string
	hits atomic.Int32
	cut  atomic.Int64 // abort the package body after this many bytes once
	srv  *httptest.Server
}

func newFeed(t *testing.T) *feed {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &feed{dir: t.TempDir(), priv: priv, pub: base64.StdEncoding.EncodeToString(pub)}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		name := filepath.Base(r.URL.Path)
		p := filepath.Join(f.dir, name)
		st, err := os.Stat(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"`+strconv.FormatInt(st.ModTime().UnixNano(), 16)+`"`)
		if n := f.cut.Swap(0); n > 0 && strings.HasSuffix(name, ".exe") {
			b, _ := os.ReadFile(p)
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.WriteHeader(200)
			w.Write(b[:n])
			// Hijack-free abort: panic with ErrAbortHandler closes the connection.
			panic(http.ErrAbortHandler)
		}
		http.ServeFile(w, r, p)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *feed) publish(t *testing.T, product, version string, body []byte, signWith ed25519.PrivateKey) *Manifest {
	name := product + "-" + version + ".exe"
	if err := os.WriteFile(filepath.Join(f.dir, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	m := &Manifest{Product: product, Version: version, File: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(f.dir, product+"-manifest.json"), b, 0o644)
	os.WriteFile(filepath.Join(f.dir, product+"-manifest.json.sig"), []byte(SignManifest(signWith, b)), 0o644)
	return m
}

func (f *feed) checker(current string) *Checker {
	return &Checker{Product: "viewer", Current: current, PublicKey: f.pub,
		FeedURL: f.srv.URL + "/updates/viewer-manifest.json", TLS: f.srv.Client().Transport.(*http.Transport).TLSClientConfig}
}

func exe(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	b[0], b[1] = 'M', 'Z'
	return b
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0}, {"1.2.10", "1.2.9", 1}, {"0.1.0-dev", "0.1.0", -1},
		{"v1.0", "1.0.0", 0}, {"2.0.0", "10.0.0", -1}, {"0.2.0-beta.2", "0.2.0-beta.1", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckAndDownload(t *testing.T) {
	f := newFeed(t)
	body := exe(3 << 20)
	f.publish(t, "viewer", "0.2.0", body, f.priv)
	c := f.checker("0.1.0")
	m, err := c.Check(context.Background(), true)
	if err != nil || m == nil || m.Version != "0.2.0" {
		t.Fatalf("check: %v %+v", err, m)
	}
	// Second check uses the ETag and still reports the update.
	m2, err := c.Check(context.Background(), true)
	if err != nil || m2 == nil {
		t.Fatalf("etag check: %v", err)
	}
	// Interrupted download resumes.
	dir := t.TempDir()
	f.cut.Store(1 << 20)
	if _, err := c.Download(context.Background(), m, dir, nil); err == nil {
		t.Fatal("expected interrupted download")
	}
	if st, err := os.Stat(filepath.Join(dir, m.File+".part")); err != nil || st.Size() == 0 {
		t.Fatalf("partial file missing: %v", err)
	}
	path, err := c.Download(context.Background(), m, dir, nil)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(body) {
		t.Fatal("downloaded content differs")
	}
}

func TestUpToDateAndNothingPublished(t *testing.T) {
	f := newFeed(t)
	if m, err := f.checker("0.1.0").Check(context.Background(), true); err != nil || m != nil {
		t.Fatalf("nothing published: %v %v", m, err)
	}
	f.publish(t, "viewer", "0.1.0", exe(1000), f.priv)
	if m, err := f.checker("0.1.0").Check(context.Background(), true); err != nil || m != nil {
		t.Fatalf("same version: %v %v", m, err)
	}
}

func TestRejectsBadSignatureWrongProductAndNoKey(t *testing.T) {
	f := newFeed(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	f.publish(t, "viewer", "0.2.0", exe(1000), other)
	if _, err := f.checker("0.1.0").Check(context.Background(), true); !errors.Is(err, ErrBadSig) {
		t.Fatalf("want bad signature, got %v", err)
	}
	f.publish(t, "viewer", "0.2.0", exe(1000), f.priv)
	// Rename product in a validly signed manifest for another product.
	c := f.checker("0.1.0")
	c.Product = "agent"
	c.FeedURL = f.srv.URL + "/updates/viewer-manifest.json"
	if _, err := c.Check(context.Background(), true); !errors.Is(err, ErrWrongProd) {
		t.Fatalf("want wrong product, got %v", err)
	}
	c = f.checker("0.1.0")
	c.PublicKey = ""
	if _, err := c.Check(context.Background(), true); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want no key, got %v", err)
	}
}

func TestRejectsTamperedAndNonExecutable(t *testing.T) {
	f := newFeed(t)
	m := f.publish(t, "viewer", "0.2.0", exe(5000), f.priv)
	// Replace the package after signing.
	os.WriteFile(filepath.Join(f.dir, m.File), exe(5000), 0o644)
	c := f.checker("0.1.0")
	m, _ = c.Check(context.Background(), true)
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), m, dir, nil); !errors.Is(err, ErrMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, m.File)); err == nil {
		t.Fatal("tampered file was kept")
	}
	notExe := []byte(strings.Repeat("x", 100))
	m = f.publish(t, "viewer", "0.3.0", notExe, f.priv)
	if _, err := c.Download(context.Background(), m, dir, nil); !errors.Is(err, ErrNotExe) {
		t.Fatalf("want not exe, got %v", err)
	}
}

func TestBackoffAndSingleFlight(t *testing.T) {
	c := &Checker{Product: "viewer", Current: "0.1.0", PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), FeedURL: "https://127.0.0.1:1/updates/viewer-manifest.json"}
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	if _, err := c.Check(context.Background(), false); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := c.Check(context.Background(), false); !errors.Is(err, ErrBackoff) {
		t.Fatalf("want backoff, got %v", err)
	}
	// A manual check is allowed during backoff.
	if _, err := c.Check(context.Background(), true); errors.Is(err, ErrBackoff) {
		t.Fatal("manual check was blocked by backoff")
	}
	c.busy = true
	if _, err := c.Check(context.Background(), true); !errors.Is(err, ErrInProgress) {
		t.Fatalf("want in progress, got %v", err)
	}
}

func TestInstallConfirmAndRollback(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "RemoteSupportViewer.exe")
	os.WriteFile(prog, []byte("MZold"), 0o755)
	staged := filepath.Join(dir, "new.exe")
	os.WriteFile(staged, []byte("MZnew"), 0o755)

	if err := Install(prog, staged, "0.1.0", "0.2.0", func() bool { return true }); !errors.Is(err, ErrBusy) {
		t.Fatalf("install during session: %v", err)
	}
	if b, _ := os.ReadFile(prog); string(b) != "MZold" {
		t.Fatal("program changed while busy")
	}
	if err := Install(prog, staged, "0.1.0", "0.2.0", func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(prog); string(b) != "MZnew" {
		t.Fatal("not installed")
	}
	if b, _ := os.ReadFile(BackupPath(prog)); string(b) != "MZold" {
		t.Fatal("no backup")
	}
	// Healthy start clears the pending record.
	if rb, err := StartupCheck(prog, "0.2.0"); rb || err != nil {
		t.Fatal(rb, err)
	}
	ConfirmHealthy(prog)
	if _, err := os.Stat(pendingPath(prog)); err == nil {
		t.Fatal("pending not cleared")
	}

	// Unhealthy: install again, never confirm, third start rolls back.
	os.WriteFile(staged, []byte("MZbad"), 0o755)
	if err := Install(prog, staged, "0.2.0", "0.3.0", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if rb, err := StartupCheck(prog, "0.3.0"); rb || err != nil {
			t.Fatalf("attempt %d: %v %v", i, rb, err)
		}
	}
	rb, err := StartupCheck(prog, "0.3.0")
	if !rb || err != nil {
		t.Fatalf("expected rollback: %v %v", rb, err)
	}
	if b, _ := os.ReadFile(prog); string(b) != "MZnew" {
		t.Fatalf("rolled back to %q", b)
	}
}
