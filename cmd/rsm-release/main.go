// Command rsm-release creates update signing keys and signed update
// manifests for a self-hosted Remote Support Manager server.
//
//	rsm-release keygen --out update-signing.key
//	    Writes a new private key (keep it offline, never commit it) and
//	    prints the public key to build into the apps.
//
//	rsm-release manifest --key update-signing.key --product viewer \
//	    --version 0.2.0 --file dist/RemoteSupportViewer-0.2.0.exe --out rsm-data/updates
//	    Copies the program into the updates folder and writes
//	    viewer-manifest.json and viewer-manifest.json.sig next to it.
//
//	rsm-release verify --pub <base64> --manifest rsm-data/updates/viewer-manifest.json
//
// Authenticode signing (for example with Azure Trusted Signing) is a
// separate step done before "manifest", because the manifest hash must be
// taken from the final, signed file.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rsm-release keygen|manifest|verify [flags]")
	os.Exit(2)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "update-signing.key", "private key file to create")
	_ = fs.Parse(args)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w (refusing to overwrite an existing key)", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println("Private key written to", *out, "- keep it offline and out of git.")
	fmt.Println("Public key (build with -ldflags \"-X github.com/TechSavage1989/remote-support-manager/internal/update.PublicKey=<key>\"):")
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("not an rsm-release private key")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	keyPath := fs.String("key", "", "private key from keygen")
	product := fs.String("product", "", "viewer or agent")
	version := fs.String("version", "", "version of the program, e.g. 0.2.0")
	file := fs.String("file", "", "the (already Authenticode-signed) .exe")
	out := fs.String("out", "", "the server's updates folder (rsm-server serve --updates)")
	notes := fs.String("notes", "", "short release notes shown to the user")
	minVer := fs.String("min-version", "", "oldest version allowed to update directly")
	_ = fs.Parse(args)
	if *keyPath == "" || (*product != "viewer" && *product != "agent") || *version == "" || *file == "" || *out == "" {
		return errors.New("manifest needs --key, --product viewer|agent, --version, --file and --out")
	}
	priv, err := loadKey(*keyPath)
	if err != nil {
		return err
	}
	src, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer src.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(src, head); err != nil || string(head) != "MZ" {
		return errors.New("--file is not a Windows executable")
	}
	_, _ = src.Seek(0, io.SeekStart)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s.exe", map[string]string{"viewer": "RemoteSupportViewer", "agent": "RemoteSupportAgent"}[*product], *version)
	dstPath := filepath.Join(*out, name)
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("%s already exists; published files are never overwritten, use a new version", dstPath)
	}
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dstPath)
		return err
	}
	m := update.Manifest{Product: *product, Version: *version, File: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)),
		Notes: *notes, Published: time.Now().UTC().Format(time.RFC3339), MinVersion: *minVer}
	body, _ := json.MarshalIndent(m, "", "  ")
	mp := filepath.Join(*out, *product+"-manifest.json")
	// Write the signature first under a temp name, then swap both, so a
	// client never sees a manifest without its matching signature for long.
	if err := os.WriteFile(mp+".sig.tmp", []byte(update.SignManifest(priv, body)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(mp+".tmp", body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(mp+".tmp", mp); err != nil {
		return err
	}
	if err := os.Rename(mp+".sig.tmp", mp+".sig"); err != nil {
		return err
	}
	fmt.Printf("Published %s %s: %s (%d bytes, sha256 %s)\n", *product, *version, name, n, m.SHA256)
	fmt.Println("Public key for this signature:", base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pub := fs.String("pub", "", "public key (base64)")
	mp := fs.String("manifest", "", "manifest file")
	_ = fs.Parse(args)
	body, err := os.ReadFile(*mp)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*mp + ".sig")
	if err != nil {
		return err
	}
	m, err := update.VerifyManifest(*pub, body, string(sig))
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(filepath.Dir(*mp), m.File))
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, _ := io.Copy(h, f)
	if n != m.Size || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return errors.New("signature is valid but the program file does not match it")
	}
	fmt.Printf("OK: %s %s, %s, signature and hash verified\n", m.Product, m.Version, m.File)
	return nil
}
