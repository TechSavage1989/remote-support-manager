package pathsafe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNamesAndPaths(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "CON", "nul.txt", "a<b", "a:b", "con.txt.bak", "trailing.", "trailing ", "x/y", `x\y`} {
		if CheckName(bad) == nil {
			t.Errorf("CheckName(%q) accepted", bad)
		}
	}
	for _, good := range []string{"report.pdf", "Ünïcødé 文件.txt", ".hidden", "console.txt"} {
		if err := CheckName(good); err != nil {
			t.Errorf("CheckName(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"../x", "a/../../x", "/abs", `C:\x`, "a//b"} {
		if CheckRel(bad) == nil {
			t.Errorf("CheckRel(%q) accepted", bad)
		}
	}
	root := t.TempDir()
	if _, err := Join(root, "../escape"); err == nil {
		t.Error("Join allowed escaping the root")
	}
	p, err := Join(root, "a/b/c.txt")
	if err != nil || filepath.Dir(filepath.Dir(filepath.Dir(p))) != root {
		t.Fatalf("Join: %v %s", err, p)
	}
	f := filepath.Join(root, "file.txt")
	os.WriteFile(f, nil, 0o600)
	if u := Unique(f); filepath.Base(u) != "file (1).txt" {
		t.Errorf("Unique: %s", u)
	}
}
