package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func healthy(addr string) bool {
	c := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c.Get("https://" + addr + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// The no-command-line flow: the server waits for the setup page, starts
// HTTPS on the local network after the first administrator is created, and
// moves behind Tailscale Funnel when that is chosen.
func TestRunnerSetupFlow(t *testing.T) {
	setup, local, tunnel := freeAddr(t), freeAddr(t), freeAddr(t)
	rn := newRunner(t.TempDir(), setup, "", "")
	rn.localListen, rn.tunnelListen, rn.httpListen = local, tunnel, "-"
	rn.logf = t.Logf
	var mu sync.Mutex
	var funnelTarget string
	rn.funnelOn = func(target string) error { mu.Lock(); funnelTarget = target; mu.Unlock(); return nil }
	rn.funnelOff = func() error { return nil }
	rn.tsName = func() (string, error) { return "pc.tail1234.ts.net", nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rn.run(ctx) }()
	defer func() { cancel(); <-done }()

	page := "http://" + setup + "/"
	waitFor(t, "setup page", func() bool {
		r, err := http.Get(page)
		if err == nil {
			r.Body.Close()
		}
		return err == nil
	})
	if healthy(local) {
		t.Fatal("HTTPS started before an administrator existed")
	}
	postForm := func(f url.Values) int {
		req, _ := http.NewRequest("POST", page, strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://"+setup)
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := postForm(url.Values{"org": {"Contoso Test"}, "mode": {"local"}, "name": {"Alex Admin"}, "email": {"alex@example.test"},
		"password": {"correct horse battery"}, "password2": {"correct horse battery"}}); code != http.StatusSeeOther {
		t.Fatalf("setup: %d", code)
	}
	waitFor(t, "local HTTPS", func() bool { return healthy(local) })
	if st := rn.getStatus(); !strings.Contains(st.ConsoleURL, "https://") {
		t.Fatalf("status: %+v", st)
	}

	if code := postForm(url.Values{"org": {"Contoso Test"}, "mode": {"tailscale"}, "email": {"alex@example.test"},
		"password": {"correct horse battery"}}); code != http.StatusSeeOther {
		t.Fatalf("switch to tailscale: %d", code)
	}
	waitFor(t, "tunnel listener", func() bool { return healthy(tunnel) })
	waitFor(t, "funnel", func() bool { mu.Lock(); defer mu.Unlock(); return funnelTarget == tunnel })
	if healthy(local) {
		t.Fatal("local-network listener still running after switching to the tunnel")
	}
	if st := rn.getStatus(); st.ConsoleURL != "https://pc.tail1234.ts.net/" || !strings.Contains(st.CertNote, "Funnel is on") {
		t.Fatalf("status: %+v", st)
	}
}

func TestSetupMustBeLoopback(t *testing.T) {
	rn := newRunner(t.TempDir(), "0.0.0.0:0", "", "")
	if err := rn.run(context.Background()); err == nil {
		t.Fatal("setup page accepted a non-loopback address")
	}
}
