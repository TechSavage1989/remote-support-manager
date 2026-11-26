package tlspin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	pin := Fingerprint(srv.Certificate())
	if _, err := HTTPClient(pin).Get(srv.URL); err != nil {
		t.Fatalf("matching pin rejected: %v", err)
	}
	wrong := strings.Repeat("ab", 32)
	if _, err := HTTPClient(wrong).Get(srv.URL); err == nil {
		t.Fatal("wrong pin accepted")
	}
	if _, err := HTTPClient("").Get(srv.URL); err == nil {
		t.Fatal("self-signed certificate accepted without a pin")
	}
	if Normalize("AB:CD ef") != Normalize("abcdef") {
		t.Error("Normalize should ignore case, colons and spaces")
	}
}

func TestGenerateSelfSigned(t *testing.T) {
	d := t.TempDir()
	fp, err := GenerateSelfSigned(filepath.Join(d, "c.crt"), filepath.Join(d, "c.key"), []string{"localhost", "127.0.0.1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FileFingerprint(filepath.Join(d, "c.crt"))
	if err != nil || got != fp {
		t.Fatalf("fingerprint mismatch: %v %s %s", err, got, fp)
	}
}
