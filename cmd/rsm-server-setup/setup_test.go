package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A clean checkout embeds only the README; unpack must skip it and the
// setup must notice the missing server.
func TestUnpackSkipsReadme(t *testing.T) {
	dir := t.TempDir()
	if err := unpack(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Fatal("README.md was unpacked")
	}
	if _, err := os.Stat(filepath.Join(dir, "rsm-server.exe")); err != nil && hasServer() {
		t.Fatal("embedded server was not unpacked")
	}
}
