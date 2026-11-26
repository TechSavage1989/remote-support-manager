// Package pathsafe validates relative paths received from a remote peer
// before they touch the local filesystem.
package pathsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var reserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// MaxComponent is the longest single name accepted.
const MaxComponent = 255

// MaxRel is the longest relative path accepted.
const MaxRel = 4096

// CheckName validates one path component using Windows rules on every
// platform, so a transfer that works in tests also works on Windows.
func CheckName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid name %q", name)
	}
	if len(name) > MaxComponent {
		return errors.New("name too long")
	}
	for _, r := range name {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("name %q contains a character Windows does not allow", name)
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("name %q ends with a dot or space", name)
	}
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reserved[strings.TrimRight(base, " ")] {
		return fmt.Errorf("name %q is reserved on Windows", name)
	}
	return nil
}

// CheckRel validates a forward-slash relative path such as "a/b/c.txt".
func CheckRel(rel string) error {
	if rel == "" || len(rel) > MaxRel {
		return errors.New("invalid relative path")
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return fmt.Errorf("path %q must be relative and use forward slashes", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if err := CheckName(part); err != nil {
			return err
		}
	}
	return nil
}

// Join validates rel and joins it under root, guaranteeing the result stays
// inside root.
func Join(root, rel string) (string, error) {
	if err := CheckRel(rel); err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(absRoot, filepath.FromSlash(rel))
	r, err := filepath.Rel(absRoot, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes destination")
	}
	return p, nil
}

// Unique returns p if nothing exists there, otherwise "name (n).ext" for the
// first free n.
func Unique(p string) string {
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return p
	}
	dir, file := filepath.Split(p)
	ext := filepath.Ext(file)
	stem := strings.TrimSuffix(file, ext)
	if ext == file { // dotfile like ".env"
		stem, ext = file, ""
	}
	for n := 1; n < 10000; n++ {
		c := filepath.Join(dir, stem+" ("+strconv.Itoa(n)+")"+ext)
		if _, err := os.Lstat(c); os.IsNotExist(err) {
			return c
		}
	}
	return filepath.Join(dir, stem+" (copy)"+ext)
}
