// Package transfer implements the session file transfer engine used by both
// the technician viewer and the endpoint agent.
//
// The same engine runs on both ends. The side that holds the files is the
// sender; the other is the receiver. A job carries a manifest of every
// directory (so nested and empty folders survive) and file. Files are sent
// one at a time in 256 KiB chunks with a sliding window, verified with
// SHA-256, and written to "<name>.rsmpart" until verified, so an interrupted
// transfer resumes from the verified partial data when the same job is run
// again. Conflicts with existing files are always decided by a person
// (overwrite, rename or skip); nothing is ever opened or executed.
package transfer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TechSavage1989/remote-support-manager/internal/pathsafe"
	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// ChunkSize is the payload size of one xf.chunk.
const ChunkSize = 256 << 10

// Window is the number of unacknowledged chunks a sender may have in flight.
const Window = 16

// PartSuffix marks unverified partial data.
const PartSuffix = ".rsmpart"

// Conflict actions.
const (
	Overwrite = "overwrite"
	Rename    = "rename"
	Skip      = "skip"
)

// Peer sends a message to the other end of the session.
type Peer interface {
	Send(hdr any, payload []byte) error
}

// Item is one manifest entry; Rel always uses forward slashes.
type Item struct {
	Rel   string `json:"rel"`
	Dir   bool   `json:"dir,omitempty"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type msgBegin struct {
	T     string `json:"t"`
	ID    string `json:"id"`
	Dest  string `json:"dest"`
	Items []Item `json:"items"`
	Total int64  `json:"total"`
}

type msgFile struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	Idx     int    `json:"idx"`
	Restart bool   `json:"restart,omitempty"`
}

type msgConflict struct {
	T            string `json:"t"`
	ID           string `json:"id"`
	Idx          int    `json:"idx"`
	Rel          string `json:"rel"`
	ExistingSize int64  `json:"existingSize"`
	ExistingTime int64  `json:"existingMtime"`
}

type msgResolve struct {
	T        string `json:"t"`
	ID       string `json:"id"`
	Idx      int    `json:"idx"`
	Action   string `json:"action"`
	ApplyAll bool   `json:"applyAll,omitempty"`
}

type msgReady struct {
	T          string `json:"t"`
	ID         string `json:"id"`
	Idx        int    `json:"idx"`
	Offset     int64  `json:"offset"`
	PartialSha string `json:"partialSha,omitempty"`
}

type msgChunk struct {
	T   string `json:"t"`
	ID  string `json:"id"`
	Idx int    `json:"idx"`
	Off int64  `json:"off"`
}

type msgAck struct {
	T   string `json:"t"`
	ID  string `json:"id"`
	Idx int    `json:"idx"`
	Off int64  `json:"off"`
}

type msgEnd struct {
	T   string `json:"t"`
	ID  string `json:"id"`
	Idx int    `json:"idx"`
	Sha string `json:"sha"`
}

type msgDone struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	Idx     int    `json:"idx"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Final   string `json:"final,omitempty"`
	Error   string `json:"error,omitempty"`
}

type msgCancel struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type msgComplete struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

type msgPull struct {
	T     string   `json:"t"`
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
}

type msgError struct {
	T     string `json:"t"`
	ID    string `json:"id"`
	Error string `json:"error"`
}

// State of a job as shown in the transfer queue.
type State string

const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateConflict    State = "waiting for decision"
	StateDone        State = "done"
	StateFailed      State = "failed"
	StateCancelled   State = "cancelled"
	StateInterrupted State = "interrupted"
)

// Progress describes one job for the visible transfer queue.
type Progress struct {
	JobID      string
	Direction  string // "send" or "receive"
	Label      string
	Dest       string
	Files      int
	FilesDone  int
	Skipped    int
	Failed     int
	BytesDone  int64
	BytesTotal int64
	Current    string
	State      State
	Error      string
}

// ConflictInfo describes an existing destination file.
type ConflictInfo struct {
	JobID        string
	Idx          int
	Rel          string
	NewSize      int64
	ExistingSize int64
	ExistingTime time.Time
}

// Engine runs transfer jobs for one session participant.
type Engine struct {
	peer Peer

	// ResolveLocally is true on the technician viewer: conflicts on this side
	// are decided by OnConflict, and conflicts reported by the peer are also
	// passed to OnConflict. On the agent it is false and conflicts are sent to
	// the viewer.
	ResolveLocally bool
	// AllowIncoming permits jobs the peer starts (uploads to the agent). The
	// viewer leaves it false so an endpoint can never push files to the
	// technician without a matching pull request.
	AllowIncoming bool
	// OnConflict must eventually call reply exactly once.
	OnConflict func(c ConflictInfo, reply func(action string, applyAll bool))
	// OnProgress is called on every state change and periodically.
	OnProgress func(Progress)
	// MarkReceived is called for each verified received file (Windows adds
	// the Mark of the Web so the file is treated as downloaded).
	MarkReceived func(path string)
	// Audit receives metadata-only events (never file contents).
	Audit func(event string, fields map[string]any)

	mu    sync.Mutex
	out   map[string]*outJob
	in    map[string]*inJob
	pulls map[string]string // job id -> local destination for pulls we asked for
	specs map[string]jobSpec
}

type jobSpec struct {
	kind  string // upload or pull
	paths []string
	dest  string
}

// New creates an engine that talks to peer.
func New(peer Peer) *Engine {
	return &Engine{
		peer:  peer,
		out:   map[string]*outJob{},
		in:    map[string]*inJob{},
		pulls: map[string]string{},
		specs: map[string]jobSpec{},
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (e *Engine) progress(p Progress) {
	if e.OnProgress != nil {
		e.OnProgress(p)
	}
}

func (e *Engine) audit(ev string, f map[string]any) {
	if e.Audit != nil {
		e.Audit(ev, f)
	}
}

// BuildManifest walks local paths into a manifest. Each selected path keeps
// its base name at the top of the destination. Symbolic links are skipped,
// never followed.
func BuildManifest(paths []string) (items []Item, srcs []string, total int64, err error) {
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		st, err := os.Lstat(p)
		if err != nil {
			return nil, nil, 0, err
		}
		base := filepath.Base(p)
		if err := pathsafe.CheckName(base); err != nil {
			return nil, nil, 0, err
		}
		if seen[strings.ToLower(base)] {
			return nil, nil, 0, fmt.Errorf("two selected items are both named %q", base)
		}
		seen[strings.ToLower(base)] = true
		if st.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		if !st.IsDir() {
			items = append(items, Item{Rel: base, Size: st.Size(), Mtime: st.ModTime().Unix()})
			srcs = append(srcs, p)
			total += st.Size()
			continue
		}
		werr := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			rel, _ := filepath.Rel(filepath.Dir(p), path)
			rel = filepath.ToSlash(rel)
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				items = append(items, Item{Rel: rel, Dir: true, Mtime: info.ModTime().Unix()})
				srcs = append(srcs, path)
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			items = append(items, Item{Rel: rel, Size: info.Size(), Mtime: info.ModTime().Unix()})
			srcs = append(srcs, path)
			total += info.Size()
			return nil
		})
		if werr != nil {
			return nil, nil, 0, werr
		}
	}
	return items, srcs, total, nil
}

// Upload sends local paths to a directory on the peer. It returns the job id.
func (e *Engine) Upload(paths []string, remoteDest string) (string, error) {
	items, srcs, total, err := BuildManifest(paths)
	if err != nil {
		return "", err
	}
	id := newID()
	e.mu.Lock()
	e.specs[id] = jobSpec{kind: "upload", paths: paths, dest: remoteDest}
	e.mu.Unlock()
	e.startOut(id, remoteDest, items, srcs, total, labelFor(paths))
	return id, nil
}

// Pull asks the peer to send its paths into localDest. It returns the job id.
func (e *Engine) Pull(remotePaths []string, localDest string) (string, error) {
	st, err := os.Stat(localDest)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("destination %q is not a folder", localDest)
	}
	id := newID()
	e.mu.Lock()
	e.pulls[id] = localDest
	e.specs[id] = jobSpec{kind: "pull", paths: remotePaths, dest: localDest}
	e.mu.Unlock()
	e.progress(Progress{JobID: id, Direction: "receive", Label: labelFor(remotePaths), Dest: localDest, State: StateQueued})
	return id, e.peer.Send(msgPull{T: protocol.TXfPull, ID: id, Paths: remotePaths}, nil)
}

// Retry runs an interrupted, failed or cancelled job again under a new id.
// Partial data verified by hash is reused, so large files resume.
func (e *Engine) Retry(jobID string) (string, error) {
	e.mu.Lock()
	spec, ok := e.specs[jobID]
	e.mu.Unlock()
	if !ok {
		return "", errors.New("unknown job")
	}
	if spec.kind == "upload" {
		return e.Upload(spec.paths, spec.dest)
	}
	return e.Pull(spec.paths, spec.dest)
}

func labelFor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	n := filepath.Base(strings.ReplaceAll(paths[0], "\\", "/"))
	if i := strings.LastIndex(paths[0], "\\"); i >= 0 && i+1 < len(paths[0]) {
		n = paths[0][i+1:]
	}
	if len(paths) > 1 {
		return fmt.Sprintf("%s and %d more", n, len(paths)-1)
	}
	return n
}

// Cancel stops a job on both ends. Verified partial data is kept so the job
// can be resumed with Retry.
func (e *Engine) Cancel(jobID string) {
	_ = e.peer.Send(msgCancel{T: protocol.TXfCancel, ID: jobID, Reason: "cancelled by user"}, nil)
	e.stopJob(jobID, StateCancelled, "cancelled")
}

// Close interrupts every job, e.g. when the session disconnects or a
// permission is revoked.
func (e *Engine) Close(reason string) {
	e.mu.Lock()
	ids := []string{}
	for id := range e.out {
		ids = append(ids, id)
	}
	for id := range e.in {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	for _, id := range ids {
		e.stopJob(id, StateInterrupted, reason)
	}
}

func (e *Engine) stopJob(id string, st State, reason string) {
	e.mu.Lock()
	oj := e.out[id]
	ij := e.in[id]
	delete(e.out, id)
	delete(e.in, id)
	e.mu.Unlock()
	if oj != nil {
		oj.stop(st, reason)
	}
	if ij != nil {
		ij.stop(st, reason)
	}
}

// Handle processes one transfer message from the peer.
func (e *Engine) Handle(m *protocol.Message) {
	var ids struct {
		ID string `json:"id"`
	}
	_ = m.Into(&ids)
	switch m.Type {
	case protocol.TXfPull:
		var p msgPull
		if m.Into(&p) != nil {
			return
		}
		e.handlePull(p)
	case protocol.TXfBegin:
		var b msgBegin
		if m.Into(&b) != nil {
			return
		}
		e.handleBegin(b)
	case protocol.TXfFile, protocol.TXfChunk, protocol.TXfEnd, protocol.TXfResolve, protocol.TXfComplete:
		e.mu.Lock()
		j := e.in[ids.ID]
		e.mu.Unlock()
		if j == nil {
			if m.Type != protocol.TXfComplete {
				_ = e.peer.Send(msgError{T: protocol.TXfError, ID: ids.ID, Error: "unknown or stopped transfer"}, nil)
			}
			return
		}
		j.handle(m)
	case protocol.TXfReady, protocol.TXfAck, protocol.TXfDone, protocol.TXfConflict:
		e.mu.Lock()
		j := e.out[ids.ID]
		e.mu.Unlock()
		if j == nil {
			return
		}
		if m.Type == protocol.TXfConflict {
			var c msgConflict
			if m.Into(&c) == nil {
				j.handleRemoteConflict(c)
			}
			return
		}
		j.deliver(m)
	case protocol.TXfCancel, protocol.TXfError:
		var c msgError
		_ = m.Into(&c)
		reason := c.Error
		st := StateFailed
		if m.Type == protocol.TXfCancel {
			var cc msgCancel
			_ = m.Into(&cc)
			reason = "cancelled by the other side"
			if cc.Reason != "" {
				reason = cc.Reason
			}
			st = StateCancelled
		}
		e.stopJob(c.ID, st, reason)
	}
}

func (e *Engine) handlePull(p msgPull) {
	items, srcs, total, err := BuildManifest(p.Paths)
	if err != nil {
		_ = e.peer.Send(msgError{T: protocol.TXfError, ID: p.ID, Error: err.Error()}, nil)
		return
	}
	e.startOut(p.ID, "", items, srcs, total, labelFor(p.Paths))
}

// ---------------------------------------------------------------- sender

type outJob struct {
	e     *Engine
	id    string
	dest  string
	items []Item
	srcs  []string
	prog  Progress
	ev    chan *protocol.Message
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	final State
	why   string
	all   string // apply-all conflict decision
}

func (e *Engine) startOut(id, dest string, items []Item, srcs []string, total int64, label string) {
	files := 0
	for _, it := range items {
		if !it.Dir {
			files++
		}
	}
	j := &outJob{e: e, id: id, dest: dest, items: items, srcs: srcs,
		ev: make(chan *protocol.Message, Window*4), done: make(chan struct{}),
		prog: Progress{JobID: id, Direction: "send", Label: label, Dest: dest, Files: files, BytesTotal: total, State: StateRunning}}
	e.mu.Lock()
	e.out[id] = j
	e.mu.Unlock()
	go j.run()
}

func (j *outJob) deliver(m *protocol.Message) {
	select {
	case j.ev <- m:
	case <-j.done:
	}
}

func (j *outJob) stop(st State, why string) {
	j.once.Do(func() {
		j.mu.Lock()
		j.final, j.why = st, why
		j.mu.Unlock()
		close(j.done)
	})
}

func (j *outJob) report() {
	j.mu.Lock()
	p := j.prog
	j.mu.Unlock()
	j.e.progress(p)
}

var errStopped = errors.New("stopped")

func (j *outJob) wait(types ...string) (*protocol.Message, error) {
	for {
		select {
		case <-j.done:
			return nil, errStopped
		case m := <-j.ev:
			for _, t := range types {
				if m.Type == t {
					return m, nil
				}
			}
		}
	}
}

func (j *outJob) handleRemoteConflict(c msgConflict) {
	j.mu.Lock()
	all := j.all
	j.prog.State = StateConflict
	j.mu.Unlock()
	reply := func(action string, applyAll bool) {
		j.mu.Lock()
		if applyAll {
			j.all = action
		}
		j.prog.State = StateRunning
		j.mu.Unlock()
		_ = j.e.peer.Send(msgResolve{T: protocol.TXfResolve, ID: j.id, Idx: c.Idx, Action: action}, nil)
	}
	if all != "" {
		reply(all, false)
		return
	}
	j.report()
	size := int64(0)
	if c.Idx >= 0 && c.Idx < len(j.items) {
		size = j.items[c.Idx].Size
	}
	if j.e.OnConflict == nil {
		reply(Skip, false)
		return
	}
	j.e.OnConflict(ConflictInfo{JobID: j.id, Idx: c.Idx, Rel: c.Rel, NewSize: size, ExistingSize: c.ExistingSize, ExistingTime: time.Unix(c.ExistingTime, 0)}, reply)
}

func (j *outJob) run() {
	err := j.runInner()
	j.e.mu.Lock()
	delete(j.e.out, j.id)
	j.e.mu.Unlock()
	j.mu.Lock()
	switch {
	case err == nil:
		j.prog.State = StateDone
		if j.prog.Failed > 0 {
			j.prog.State = StateFailed
			j.prog.Error = fmt.Sprintf("%d file(s) failed", j.prog.Failed)
		}
	case errors.Is(err, errStopped):
		j.prog.State = j.final
		j.prog.Error = j.why
	default:
		j.prog.State = StateFailed
		j.prog.Error = err.Error()
	}
	p := j.prog
	j.mu.Unlock()
	j.e.progress(p)
	j.e.audit("transfer.send."+string(p.State), map[string]any{"job": j.id, "files": p.Files, "bytes": p.BytesDone, "label": p.Label, "skipped": p.Skipped, "failed": p.Failed})
}

func (j *outJob) runInner() error {
	if err := j.e.peer.Send(msgBegin{T: protocol.TXfBegin, ID: j.id, Dest: j.dest, Items: j.items, Total: j.prog.BytesTotal}, nil); err != nil {
		return err
	}
	j.report()
	for idx, it := range j.items {
		if it.Dir {
			continue
		}
		j.mu.Lock()
		j.prog.Current = it.Rel
		j.mu.Unlock()
		if err := j.sendFile(idx, it); err != nil {
			return err
		}
	}
	return j.e.peer.Send(msgComplete{T: protocol.TXfComplete, ID: j.id}, nil)
}

func (j *outJob) sendFile(idx int, it Item) error {
	f, err := os.Open(j.srcs[idx])
	if err != nil {
		j.mu.Lock()
		j.prog.Failed++
		j.mu.Unlock()
		_ = j.e.peer.Send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, OK: false, Error: "source unreadable"}, nil)
		return nil
	}
	defer f.Close()
	restart := false
	base := j.prog.BytesDone
	for attempt := 0; attempt < 3; attempt++ {
		if err := j.e.peer.Send(msgFile{T: protocol.TXfFile, ID: j.id, Idx: idx, Restart: restart}, nil); err != nil {
			return err
		}
		m, err := j.wait(protocol.TXfReady, protocol.TXfDone)
		if err != nil {
			return err
		}
		if m.Type == protocol.TXfDone {
			var d msgDone
			_ = m.Into(&d)
			j.mu.Lock()
			if d.Skipped {
				j.prog.Skipped++
			} else if !d.OK {
				j.prog.Failed++
			}
			j.prog.BytesDone = base + it.Size
			j.mu.Unlock()
			j.report()
			return nil
		}
		var r msgReady
		_ = m.Into(&r)
		h := sha256.New()
		if r.Offset > 0 {
			if r.Offset > it.Size {
				restart = true
				continue
			}
			if _, err := io.CopyN(h, f, r.Offset); err != nil {
				return err
			}
			if hex.EncodeToString(h.Sum(nil)) != r.PartialSha {
				restart = true // partial data differs from our file: start over
				continue
			}
		}
		if err := j.stream(idx, f, r.Offset, it.Size, h, base); err != nil {
			return err
		}
		sum := hex.EncodeToString(h.Sum(nil))
		if err := j.e.peer.Send(msgEnd{T: protocol.TXfEnd, ID: j.id, Idx: idx, Sha: sum}, nil); err != nil {
			return err
		}
		m, err = j.wait(protocol.TXfDone)
		if err != nil {
			return err
		}
		var d msgDone
		_ = m.Into(&d)
		if d.OK {
			j.mu.Lock()
			j.prog.FilesDone++
			j.prog.BytesDone = base + it.Size
			j.mu.Unlock()
			j.report()
			return nil
		}
		restart = true // integrity failure: one clean retry
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	j.mu.Lock()
	j.prog.Failed++
	j.mu.Unlock()
	return nil
}

func (j *outJob) stream(idx int, f *os.File, off, size int64, h hash.Hash, base int64) error {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, ChunkSize)
	inflight := 0
	acked := off
	lastBytes := off
	lastReport := time.Now()
	for off < size || inflight > 0 {
		for off < size && inflight < Window {
			n, err := io.ReadFull(f, buf[:min64(ChunkSize, size-off)])
			if err != nil {
				return fmt.Errorf("read %s: %w", j.items[idx].Rel, err)
			}
			h.Write(buf[:n])
			if err := j.e.peer.Send(msgChunk{T: protocol.TXfChunk, ID: j.id, Idx: idx, Off: off}, buf[:n]); err != nil {
				return err
			}
			off += int64(n)
			inflight++
		}
		m, err := j.wait(protocol.TXfAck, protocol.TXfDone)
		if err != nil {
			return err
		}
		if m.Type == protocol.TXfDone {
			var d msgDone
			_ = m.Into(&d)
			return fmt.Errorf("receiver stopped: %s", d.Error)
		}
		var a msgAck
		_ = m.Into(&a)
		if a.Idx != idx {
			continue
		}
		inflight--
		acked = a.Off
		if time.Since(lastReport) > 150*time.Millisecond || acked-lastBytes >= 2<<20 {
			j.mu.Lock()
			j.prog.BytesDone = base + acked
			j.mu.Unlock()
			j.report()
			lastReport, lastBytes = time.Now(), acked
		}
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------- receiver

type inJob struct {
	e     *Engine
	id    string
	dest  string
	items []Item
	prog  Progress
	mu    sync.Mutex
	all   string

	cur       int
	f         *os.File
	part      string
	final     string
	written   int64
	h         hash.Hash
	overwrite bool
	pending   map[int]func(string) // awaiting conflict decision
	stopped   bool
}

func (e *Engine) handleBegin(b msgBegin) {
	e.mu.Lock()
	dest, isPull := e.pulls[b.ID]
	delete(e.pulls, b.ID)
	e.mu.Unlock()
	fail := func(msg string) {
		_ = e.peer.Send(msgError{T: protocol.TXfError, ID: b.ID, Error: msg}, nil)
	}
	if !isPull {
		if !e.AllowIncoming {
			fail("this side does not accept unrequested transfers")
			e.audit("transfer.rejected", map[string]any{"job": b.ID, "reason": "unrequested"})
			return
		}
		dest = b.Dest
	}
	st, err := os.Stat(dest)
	if err != nil || !st.IsDir() {
		fail("destination folder does not exist")
		return
	}
	files := 0
	var total int64
	for _, it := range b.Items {
		if err := pathsafe.CheckRel(it.Rel); err != nil {
			fail("unsafe path in transfer: " + err.Error())
			e.audit("transfer.rejected", map[string]any{"job": b.ID, "reason": "unsafe path"})
			return
		}
		if it.Size < 0 {
			fail("bad size")
			return
		}
		if !it.Dir {
			files++
			total += it.Size
		}
	}
	// Create every directory first so empty folders are preserved.
	dirs := []Item{}
	for _, it := range b.Items {
		if it.Dir {
			dirs = append(dirs, it)
		}
	}
	sort.Slice(dirs, func(a, c int) bool { return len(dirs[a].Rel) < len(dirs[c].Rel) })
	for _, d := range dirs {
		p, err := pathsafe.Join(dest, d.Rel)
		if err != nil {
			fail(err.Error())
			return
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			fail("cannot create folder " + d.Rel)
			return
		}
	}
	j := &inJob{e: e, id: b.ID, dest: dest, items: b.Items, cur: -1, pending: map[int]func(string){},
		prog: Progress{JobID: b.ID, Direction: "receive", Dest: dest, Files: files, BytesTotal: total, State: StateRunning, Label: topLabel(b.Items)}}
	e.mu.Lock()
	e.in[b.ID] = j
	e.mu.Unlock()
	e.progress(j.prog)
	e.audit("transfer.receive.start", map[string]any{"job": b.ID, "files": files, "bytes": total, "dest": dest})
}

func topLabel(items []Item) string {
	tops := []string{}
	seen := map[string]bool{}
	for _, it := range items {
		t := strings.SplitN(it.Rel, "/", 2)[0]
		if !seen[t] {
			seen[t] = true
			tops = append(tops, t)
		}
	}
	return labelFor(tops)
}

func (j *inJob) send(h any, p []byte) { _ = j.e.peer.Send(h, p) }

func (j *inJob) stop(st State, why string) {
	j.mu.Lock()
	if j.stopped {
		j.mu.Unlock()
		return
	}
	j.stopped = true
	if j.f != nil {
		_ = j.f.Sync()
		_ = j.f.Close()
		j.f = nil
	}
	j.prog.State = st
	j.prog.Error = why
	p := j.prog
	j.mu.Unlock()
	j.e.progress(p)
	j.e.audit("transfer.receive."+string(st), map[string]any{"job": j.id, "filesDone": p.FilesDone, "bytes": p.BytesDone})
}

func (j *inJob) handle(m *protocol.Message) {
	switch m.Type {
	case protocol.TXfFile:
		var f msgFile
		if m.Into(&f) == nil {
			j.onFile(f)
		}
	case protocol.TXfResolve:
		var r msgResolve
		if m.Into(&r) == nil {
			j.mu.Lock()
			cb := j.pending[r.Idx]
			delete(j.pending, r.Idx)
			j.mu.Unlock()
			if cb != nil {
				cb(r.Action)
			}
		}
	case protocol.TXfChunk:
		var c msgChunk
		if m.Into(&c) == nil {
			j.onChunk(c, m.Payload)
		}
	case protocol.TXfEnd:
		var e msgEnd
		if m.Into(&e) == nil {
			j.onEnd(e)
		}
	case protocol.TXfComplete:
		j.e.mu.Lock()
		delete(j.e.in, j.id)
		j.e.mu.Unlock()
		j.mu.Lock()
		j.prog.State = StateDone
		if j.prog.Failed > 0 {
			j.prog.State = StateFailed
			j.prog.Error = fmt.Sprintf("%d file(s) failed", j.prog.Failed)
		}
		j.prog.Current = ""
		p := j.prog
		j.mu.Unlock()
		j.e.progress(p)
		j.e.audit("transfer.receive.complete", map[string]any{"job": j.id, "filesDone": p.FilesDone, "skipped": p.Skipped, "failed": p.Failed, "bytes": p.BytesDone})
	}
}

func (j *inJob) onFile(f msgFile) {
	if f.Idx < 0 || f.Idx >= len(j.items) || j.items[f.Idx].Dir {
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: f.Idx, Error: "bad index"}, nil)
		return
	}
	it := j.items[f.Idx]
	target, err := pathsafe.Join(j.dest, it.Rel)
	if err != nil {
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: f.Idx, Error: err.Error()}, nil)
		return
	}
	j.mu.Lock()
	j.prog.Current = it.Rel
	j.mu.Unlock()
	if f.Restart {
		// Restart with the decision already taken for this file.
		j.mu.Lock()
		same := j.cur == f.Idx && j.final != ""
		final, ow := j.final, j.overwrite
		j.mu.Unlock()
		if same {
			j.open(f.Idx, final, ow, true)
			return
		}
	}
	st, err := os.Lstat(target)
	if err == nil {
		if st.IsDir() {
			j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: f.Idx, Error: "a folder with that name already exists"}, nil)
			return
		}
		j.askConflict(f.Idx, target, it, st, f.Restart)
		return
	}
	j.open(f.Idx, target, false, f.Restart)
}

func (j *inJob) askConflict(idx int, target string, it Item, st os.FileInfo, restart bool) {
	decide := func(action string) {
		switch action {
		case Overwrite:
			j.open(idx, target, true, restart)
		case Rename:
			j.open(idx, uniqueFinal(target), false, restart)
		default:
			j.mu.Lock()
			j.prog.Skipped++
			j.prog.BytesDone += it.Size
			j.prog.State = StateRunning
			p := j.prog
			j.mu.Unlock()
			j.e.progress(p)
			j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, OK: true, Skipped: true}, nil)
		}
	}
	j.mu.Lock()
	all := j.all
	j.mu.Unlock()
	if all != "" {
		decide(all)
		return
	}
	if !j.e.ResolveLocally {
		j.mu.Lock()
		j.pending[idx] = decide
		j.mu.Unlock()
		j.send(msgConflict{T: protocol.TXfConflict, ID: j.id, Idx: idx, Rel: it.Rel, ExistingSize: st.Size(), ExistingTime: st.ModTime().Unix()}, nil)
		return
	}
	if j.e.OnConflict == nil {
		decide(Skip)
		return
	}
	j.mu.Lock()
	j.prog.State = StateConflict
	p := j.prog
	j.mu.Unlock()
	j.e.progress(p)
	j.e.OnConflict(ConflictInfo{JobID: j.id, Idx: idx, Rel: it.Rel, NewSize: it.Size, ExistingSize: st.Size(), ExistingTime: st.ModTime()},
		func(action string, applyAll bool) {
			if applyAll {
				j.mu.Lock()
				j.all = action
				j.mu.Unlock()
			}
			j.mu.Lock()
			j.prog.State = StateRunning
			j.mu.Unlock()
			decide(action)
		})
}

// uniqueFinal picks a free name, also avoiding names whose partial data
// belongs to a different file.
func uniqueFinal(target string) string {
	return pathsafe.Unique(target)
}

func (j *inJob) open(idx int, final string, overwrite, restart bool) {
	part := final + PartSuffix
	j.mu.Lock()
	if j.f != nil {
		_ = j.f.Close()
		j.f = nil
	}
	j.mu.Unlock()
	if restart {
		_ = os.Remove(part)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, Error: "cannot write destination file"}, nil)
		return
	}
	st, _ := f.Stat()
	off := st.Size()
	size := j.items[idx].Size
	h := sha256.New()
	if off > size {
		_ = f.Truncate(0)
		off = 0
	}
	if off > 0 {
		if _, err := io.CopyN(h, f, off); err != nil {
			_ = f.Truncate(0)
			off = 0
			h = sha256.New()
		}
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		_ = f.Close()
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, Error: "seek failed"}, nil)
		return
	}
	partialSha := ""
	if off > 0 {
		partialSha = hex.EncodeToString(h.Sum(nil))
	}
	j.mu.Lock()
	j.cur, j.f, j.part, j.final, j.written, j.h, j.overwrite = idx, f, part, final, off, h, overwrite
	j.mu.Unlock()
	if off > 0 {
		j.e.audit("transfer.resume", map[string]any{"job": j.id, "offset": off})
	}
	j.send(msgReady{T: protocol.TXfReady, ID: j.id, Idx: idx, Offset: off, PartialSha: partialSha}, nil)
}

func (j *inJob) onChunk(c msgChunk, data []byte) {
	fail := ""
	j.mu.Lock()
	switch {
	case j.f == nil || c.Idx != j.cur || c.Off != j.written:
		fail = "out of order data"
	case j.written+int64(len(data)) > j.items[c.Idx].Size:
		fail = "more data than announced"
	default:
		if _, err := j.f.Write(data); err != nil {
			fail = "write failed (disk full?)"
		} else {
			j.h.Write(data)
			j.written += int64(len(data))
			j.prog.BytesDone += int64(len(data))
		}
	}
	p := j.prog
	off := j.written
	j.mu.Unlock()
	if fail != "" {
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: c.Idx, Error: fail}, nil)
		return
	}
	j.send(msgAck{T: protocol.TXfAck, ID: j.id, Idx: c.Idx, Off: off}, nil)
	j.e.progress(p)
}

func (j *inJob) onEnd(e msgEnd) {
	j.mu.Lock()
	f, part, final, ow, h, idx := j.f, j.part, j.final, j.overwrite, j.h, j.cur
	written := j.written
	j.f = nil
	j.mu.Unlock()
	if f == nil || e.Idx != idx {
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: e.Idx, Error: "no file in progress"}, nil)
		return
	}
	it := j.items[idx]
	syncErr := f.Sync()
	_ = f.Close()
	sum := hex.EncodeToString(h.Sum(nil))
	if syncErr != nil || written != it.Size || sum != e.Sha {
		_ = os.Remove(part)
		j.mu.Lock()
		j.prog.BytesDone -= written
		j.mu.Unlock()
		j.e.audit("transfer.integrity_failed", map[string]any{"job": j.id, "rel": it.Rel})
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, Error: "integrity check failed"}, nil)
		return
	}
	if !ow {
		if _, err := os.Lstat(final); err == nil {
			final = pathsafe.Unique(final) // appeared meanwhile: never overwrite without consent
		}
	}
	if err := os.Rename(part, final); err != nil {
		j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, Error: "could not finish file"}, nil)
		return
	}
	mt := time.Unix(it.Mtime, 0)
	_ = os.Chtimes(final, mt, mt)
	if j.e.MarkReceived != nil {
		j.e.MarkReceived(final)
	}
	rel, _ := filepath.Rel(j.dest, final)
	j.mu.Lock()
	j.prog.FilesDone++
	j.final = ""
	p := j.prog
	j.mu.Unlock()
	j.e.progress(p)
	j.send(msgDone{T: protocol.TXfDone, ID: j.id, Idx: idx, OK: true, Final: filepath.ToSlash(rel)}, nil)
}
