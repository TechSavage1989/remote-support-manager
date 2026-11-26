package transfer

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// pipe delivers encoded frames to the other engine in order, like a socket.
type pipe struct {
	to     *Engine
	q      chan []byte
	cutAt  int64 // drop everything after this many chunks (0 = never)
	chunks atomic.Int64
	cut    atomic.Bool
	mu     sync.Mutex
	closed bool
}

func (p *pipe) close() {
	p.mu.Lock()
	p.closed = true
	close(p.q)
	p.mu.Unlock()
}

func (p *pipe) Send(h any, payload []byte) error {
	if p.cut.Load() {
		return os.ErrClosed
	}
	b, err := protocol.Encode(h, payload)
	if err != nil {
		return err
	}
	m, _ := protocol.Decode(b)
	if m.Type == protocol.TXfChunk && p.cutAt > 0 && p.chunks.Add(1) > p.cutAt {
		p.cut.Store(true)
		return os.ErrClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return os.ErrClosed
	}
	p.q <- b
	return nil
}

func (p *pipe) run() {
	for b := range p.q {
		m, err := protocol.Decode(b)
		if err != nil {
			panic(err)
		}
		p.to.Handle(m)
	}
}

type pair struct {
	viewer, agent *Engine
	v2a, a2v      *pipe
	mu            sync.Mutex
	vProg         map[string]Progress
	aProg         map[string]Progress
}

func newPair(t *testing.T, onConflict func(ConflictInfo, func(string, bool))) *pair {
	p := &pair{vProg: map[string]Progress{}, aProg: map[string]Progress{}}
	p.v2a = &pipe{q: make(chan []byte, 1024)}
	p.a2v = &pipe{q: make(chan []byte, 1024)}
	p.viewer = New(p.v2a)
	p.agent = New(p.a2v)
	p.v2a.to = p.agent
	p.a2v.to = p.viewer
	p.viewer.ResolveLocally = true
	p.agent.AllowIncoming = true
	p.viewer.OnConflict = onConflict
	p.viewer.OnProgress = func(pr Progress) { p.mu.Lock(); p.vProg[pr.JobID] = pr; p.mu.Unlock() }
	p.agent.OnProgress = func(pr Progress) { p.mu.Lock(); p.aProg[pr.JobID] = pr; p.mu.Unlock() }
	go p.v2a.run()
	go p.a2v.run()
	t.Cleanup(func() { p.v2a.close(); p.a2v.close() })
	return p
}

func (p *pair) waitState(t *testing.T, viewerSide bool, id string, want ...State) Progress {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		m := p.aProg
		if viewerSide {
			m = p.vProg
		}
		pr, ok := m[id]
		p.mu.Unlock()
		if ok {
			for _, w := range want {
				if pr.State == w {
					return pr
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	t.Fatalf("job %s never reached %v (viewer=%+v agent=%+v)", id, want, p.vProg[id], p.aProg[id])
	return Progress{}
}

func writeRandom(t *testing.T, path string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, _ = rand.Read(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func makeTree(t *testing.T, root string) map[string][]byte {
	files := map[string][]byte{
		"Project/readme.txt":         []byte("hello\r\nworld ✓"),
		"Project/src/main.go":        []byte("package main"),
		"Project/src/deep/a/b/c.bin": nil,
		"Project/big.dat":            nil,
		"Project/zero.txt":           {},
	}
	for rel, b := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if b == nil {
			size := 1500
			if rel == "Project/big.dat" {
				size = 9*ChunkSize + 1234
			}
			files[rel] = writeRandom(t, p, size)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"Project/empty", "Project/src/deep/empty-too"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func checkTree(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("content mismatch for %s", rel)
		}
	}
	for _, d := range []string{"Project/empty", "Project/src/deep/empty-too"} {
		st, err := os.Stat(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil || !st.IsDir() {
			t.Fatalf("empty folder %s not preserved", d)
		}
	}
	_ = filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error {
		if filepath.Ext(p) == PartSuffix {
			t.Fatalf("partial file left behind: %s", p)
		}
		return nil
	})
}

func TestUploadNestedFoldersAndLargeFile(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	files := makeTree(t, src)
	p := newPair(t, nil)
	var marked atomic.Int32
	p.agent.MarkReceived = func(string) { marked.Add(1) }
	id, err := p.viewer.Upload([]string{filepath.Join(src, "Project")}, dst)
	if err != nil {
		t.Fatal(err)
	}
	pr := p.waitState(t, true, id, StateDone, StateFailed)
	if pr.State != StateDone || pr.FilesDone != len(files) {
		t.Fatalf("upload: %+v", pr)
	}
	checkTree(t, dst, files)
	if int(marked.Load()) != len(files) {
		t.Fatalf("MarkReceived called %d times, want %d", marked.Load(), len(files))
	}
}

func TestDownloadNestedFolders(t *testing.T) {
	remote, local := t.TempDir(), t.TempDir()
	files := makeTree(t, remote)
	p := newPair(t, nil)
	id, err := p.viewer.Pull([]string{filepath.Join(remote, "Project")}, local)
	if err != nil {
		t.Fatal(err)
	}
	pr := p.waitState(t, true, id, StateDone, StateFailed)
	if pr.State != StateDone {
		t.Fatalf("download: %+v", pr)
	}
	checkTree(t, local, files)
}

func TestInterruptedTransferResumes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	want := writeRandom(t, filepath.Join(src, "large.iso"), 40*ChunkSize+77)
	p := newPair(t, nil)
	p.v2a.cutAt = 12 // connection drops after 12 chunks
	id, err := p.viewer.Upload([]string{filepath.Join(src, "large.iso")}, dst)
	if err != nil {
		t.Fatal(err)
	}
	pr := p.waitState(t, true, id, StateFailed, StateDone)
	if pr.State != StateFailed {
		t.Fatalf("expected failure after cut, got %+v", pr)
	}
	// Session drops; both sides interrupt jobs.
	p.agent.Close("disconnected")
	time.Sleep(50 * time.Millisecond)
	part := filepath.Join(dst, "large.iso"+PartSuffix)
	st, err := os.Stat(part)
	if err != nil || st.Size() == 0 {
		t.Fatalf("expected partial data to be kept: %v", err)
	}
	kept := st.Size()
	// Reconnect: a new pipe pair would be created; reuse with cut disabled.
	p.v2a.cut.Store(false)
	p.v2a.cutAt = 0
	var resumed atomic.Int64
	p.agent.Audit = func(ev string, f map[string]any) {
		if ev == "transfer.resume" {
			resumed.Store(f["offset"].(int64))
		}
	}
	id2, err := p.viewer.Retry(id)
	if err != nil {
		t.Fatal(err)
	}
	pr = p.waitState(t, true, id2, StateDone, StateFailed)
	if pr.State != StateDone {
		t.Fatalf("resume: %+v", pr)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "large.iso"))
	if !bytes.Equal(got, want) {
		t.Fatal("resumed file differs")
	}
	if resumed.Load() != kept {
		t.Fatalf("resume offset %d, want %d", resumed.Load(), kept)
	}
}

func TestCorruptPartialRestartsCleanly(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	want := writeRandom(t, filepath.Join(src, "f.bin"), 3*ChunkSize)
	// A stale partial from a different file with the same name.
	_ = os.WriteFile(filepath.Join(dst, "f.bin"+PartSuffix), []byte("not the same data"), 0o644)
	p := newPair(t, nil)
	id, _ := p.viewer.Upload([]string{filepath.Join(src, "f.bin")}, dst)
	pr := p.waitState(t, true, id, StateDone, StateFailed)
	if pr.State != StateDone {
		t.Fatalf("%+v", pr)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "f.bin"))
	if !bytes.Equal(got, want) {
		t.Fatal("content mismatch")
	}
}

func TestConflictChoices(t *testing.T) {
	for _, action := range []string{Overwrite, Rename, Skip} {
		t.Run(action, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			_ = os.WriteFile(filepath.Join(src, "report.txt"), []byte("new"), 0o644)
			_ = os.WriteFile(filepath.Join(dst, "report.txt"), []byte("old"), 0o644)
			asked := 0
			p := newPair(t, func(c ConflictInfo, reply func(string, bool)) {
				asked++
				if c.Rel != "report.txt" || c.ExistingSize != 3 {
					t.Errorf("bad conflict info %+v", c)
				}
				reply(action, false)
			})
			id, _ := p.viewer.Upload([]string{filepath.Join(src, "report.txt")}, dst)
			pr := p.waitState(t, true, id, StateDone, StateFailed)
			if asked != 1 {
				t.Fatalf("asked %d times", asked)
			}
			old, _ := os.ReadFile(filepath.Join(dst, "report.txt"))
			renamed, rerr := os.ReadFile(filepath.Join(dst, "report (1).txt"))
			switch action {
			case Overwrite:
				if string(old) != "new" || rerr == nil {
					t.Fatal("overwrite failed")
				}
			case Rename:
				if string(old) != "old" || string(renamed) != "new" {
					t.Fatal("rename failed")
				}
			case Skip:
				if string(old) != "old" || rerr == nil || pr.Skipped != 1 {
					t.Fatalf("skip failed %+v", pr)
				}
			}
		})
	}
}

func TestDownloadConflictResolvedLocally(t *testing.T) {
	remote, local := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(remote, "a.txt"), []byte("remote"), 0o644)
	_ = os.WriteFile(filepath.Join(local, "a.txt"), []byte("local"), 0o644)
	p := newPair(t, func(c ConflictInfo, reply func(string, bool)) { reply(Rename, true) })
	id, _ := p.viewer.Pull([]string{filepath.Join(remote, "a.txt")}, local)
	p.waitState(t, true, id, StateDone)
	b, _ := os.ReadFile(filepath.Join(local, "a (1).txt"))
	if string(b) != "remote" {
		t.Fatal("download rename failed")
	}
}

func TestUnrequestedPushRejectedByViewer(t *testing.T) {
	src, local := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "evil.exe"), []byte("MZ"), 0o644)
	p := newPair(t, nil)
	// The agent tries to push a file to the technician without a pull.
	id, err := p.agent.Upload([]string{filepath.Join(src, "evil.exe")}, local)
	if err != nil {
		t.Fatal(err)
	}
	pr := p.waitState(t, false, id, StateFailed)
	if pr.State != StateFailed {
		t.Fatal("expected rejection")
	}
	if _, err := os.Stat(filepath.Join(local, "evil.exe")); err == nil {
		t.Fatal("file was written")
	}
}

func TestUnsafeManifestRejected(t *testing.T) {
	dst := t.TempDir()
	p := newPair(t, nil)
	var errSeen atomic.Bool
	p.viewer.OnProgress = func(Progress) {}
	// Craft a malicious begin from the "viewer" to the agent.
	for _, rel := range []string{"../escape.txt", "a/../../x", "C:/Windows/x", "CON", "a\\b", "ok/name:stream"} {
		b, _ := protocol.Encode(msgBegin{T: protocol.TXfBegin, ID: "bad" + rel, Dest: dst, Items: []Item{{Rel: rel, Size: 1}}}, nil)
		m, _ := protocol.Decode(b)
		p.agent.Handle(m)
		p.agent.mu.Lock()
		if p.agent.in["bad"+rel] != nil {
			t.Fatalf("accepted unsafe path %q", rel)
		}
		p.agent.mu.Unlock()
	}
	errSeen.Store(true)
	entries, _ := os.ReadDir(filepath.Dir(dst))
	for _, e := range entries {
		if e.Name() == "escape.txt" {
			t.Fatal("escaped destination")
		}
	}
}

func TestCancelKeepsPartialForResume(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeRandom(t, filepath.Join(src, "big.bin"), 200*ChunkSize)
	p := newPair(t, nil)
	id, _ := p.viewer.Upload([]string{filepath.Join(src, "big.bin")}, dst)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		done := p.aProg[id].BytesDone
		p.mu.Unlock()
		if done > 4*ChunkSize {
			break
		}
		time.Sleep(time.Millisecond)
	}
	p.viewer.Cancel(id)
	pr := p.waitState(t, true, id, StateCancelled, StateDone)
	if pr.State == StateDone {
		t.Skip("transfer finished before cancel on this machine")
	}
	p.waitState(t, false, id, StateCancelled)
	if _, err := os.Stat(filepath.Join(dst, "big.bin")); err == nil {
		t.Fatal("cancelled file must not be finalized")
	}
	id2, _ := p.viewer.Retry(id)
	if pr := p.waitState(t, true, id2, StateDone, StateFailed); pr.State != StateDone {
		t.Fatalf("%+v", pr)
	}
}
