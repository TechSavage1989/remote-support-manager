package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Tailscale Funnel is the free, no-router way to reach the server from the
// internet: Tailscale gives the computer a permanent name.ts.net address
// with a public certificate and forwards HTTPS for it to the server's
// loopback listener. The setup page only reads the address and turns
// Funnel on or off; installing Tailscale and signing in stay with the person.

func tailscaleExe() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if d := os.Getenv(env); d != "" {
				p := filepath.Join(d, "Tailscale", "tailscale.exe")
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
			}
		}
	}
	return "", errors.New("Tailscale is not installed")
}

func tailscaleCmd(timeout time.Duration, args ...string) ([]byte, error) {
	exe, err := tailscaleExe()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// tailscaleName returns this computer's Tailscale DNS name.
func tailscaleName() (string, error) {
	out, err := tailscaleCmd(10*time.Second, "status", "--json")
	if err != nil {
		return "", err
	}
	var st struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", errors.New("could not read Tailscale's status")
	}
	if st.BackendState != "Running" {
		return "", errors.New("Tailscale is not signed in or not connected")
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" {
		return "", errors.New("Tailscale has no address for this computer yet")
	}
	return name, nil
}

// tailscaleFunnelOn forwards public HTTPS on the Tailscale address to the
// server's loopback listener. The listener uses a self-signed certificate,
// hence https+insecure (the hop never leaves this computer).
func tailscaleFunnelOn(target string) error {
	// When Funnel is not yet allowed for the account, tailscale prints a
	// login.tailscale.com link and waits; the timeout ends that and the
	// link is shown on the setup page.
	_, err := tailscaleCmd(45*time.Second, "funnel", "--bg", "https+insecure://"+target)
	return err
}

var tsLinkRE = regexp.MustCompile(`https://login\.tailscale\.com/[A-Za-z0-9/?=&._%-]+`)

// tailscaleLink finds an approval link in tailscale's output.
func tailscaleLink(msg string) string { return tsLinkRE.FindString(msg) }

// tailscaleFunnelOff removes the forwarding added above.
func tailscaleFunnelOff() error {
	_, err := tailscaleCmd(30*time.Second, "funnel", "reset")
	return err
}
