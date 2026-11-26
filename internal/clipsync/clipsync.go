// Package clipsync keeps the technician's and the endpoint's clipboards in
// step during a session.
//
// It never logs clipboard contents: audit events carry only the formats and
// sizes. Loops are prevented by remembering the hash of what was last
// written from the peer, and stale data is dropped after a reconnect by
// tagging every message with the session epoch.
package clipsync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// MaxBytes is the largest clipboard payload synced (all formats together).
const MaxBytes = 7 << 20

// Directions.
const (
	Both     = "both"
	ToRemote = "to-remote" // technician -> endpoint
	ToLocal  = "to-local"  // endpoint -> technician
)

// Clip is one clipboard snapshot.
type Clip struct {
	Text string // Unicode text, any line endings
	RTF  []byte
	HTML []byte // CF_HTML fragment, UTF-8
	PNG  []byte
	// Files is a list of copied paths. They are never sent as clipboard
	// data; they are offered to the transfer queue instead.
	Files []string
}

// Empty reports whether nothing syncable is present.
func (c Clip) Empty() bool {
	return c.Text == "" && len(c.RTF) == 0 && len(c.HTML) == 0 && len(c.PNG) == 0 && len(c.Files) == 0
}

// Hash identifies the content for loop prevention.
func (c Clip) Hash() string {
	h := sha256.New()
	write := func(tag string, b []byte) {
		h.Write([]byte(tag))
		var n [8]byte
		l := len(b)
		for i := 0; i < 8; i++ {
			n[i] = byte(l >> (8 * i))
		}
		h.Write(n[:])
		h.Write(b)
	}
	write("t", []byte(c.Text))
	write("r", c.RTF)
	write("h", c.HTML)
	write("p", c.PNG)
	for _, f := range c.Files {
		write("f", []byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Peer sends a frame to the other side.
type Peer interface {
	Send(hdr any, payload []byte) error
}

// Sync is one side of clipboard synchronisation.
type Sync struct {
	peer     Peer
	isViewer bool

	// Apply writes a clip received from the peer to the local clipboard.
	Apply func(Clip) error
	// OnFiles is called when the peer copied files; the receiver decides
	// whether to queue a transfer (it must still pass permission checks).
	OnFiles func(paths []string)
	// Audit receives metadata only.
	Audit func(event string, fields map[string]any)

	mu          sync.Mutex
	enabled     bool
	direction   string
	epoch       int
	seq         int64
	lastApplied string
	lastSent    string
	stopped     bool
}

// New creates a Sync. isViewer selects which direction counts as sending.
// Sync starts disabled until Configure is called.
func New(peer Peer, isViewer bool) *Sync {
	return &Sync{peer: peer, isViewer: isViewer, direction: Both}
}

// Configure sets the toggle and direction. On the viewer it also tells the
// agent, which follows the technician's choice.
func (s *Sync) Configure(enabled bool, direction string) error {
	if direction != Both && direction != ToRemote && direction != ToLocal {
		return errors.New("clipsync: unknown direction")
	}
	s.mu.Lock()
	s.enabled = enabled && !s.stopped
	s.direction = direction
	en := s.enabled
	s.mu.Unlock()
	if s.isViewer {
		return s.peer.Send(protocol.ClipConfig{T: protocol.TClipConfig, Enabled: en, Direction: direction}, nil)
	}
	return nil
}

// State returns the current toggle and direction.
func (s *Sync) State() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.direction
}

func (s *Sync) canSend() bool {
	if !s.enabled || s.stopped {
		return false
	}
	if s.direction == Both {
		return true
	}
	if s.isViewer {
		return s.direction == ToRemote
	}
	return s.direction == ToLocal
}

func (s *Sync) canReceive() bool {
	if !s.enabled || s.stopped {
		return false
	}
	if s.direction == Both {
		return true
	}
	if s.isViewer {
		return s.direction == ToLocal
	}
	return s.direction == ToRemote
}

// SetEpoch is called when the session (re)starts. Messages tagged with
// another epoch are dropped, so nothing queued before a reconnect is pasted
// afterwards, and the loop-prevention memory is reset.
func (s *Sync) SetEpoch(epoch int) {
	s.mu.Lock()
	s.epoch = epoch
	s.lastApplied, s.lastSent = "", ""
	s.mu.Unlock()
}

// Stop ends synchronisation for good (session end).
func (s *Sync) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.enabled = false
	s.mu.Unlock()
}

// LocalChanged is called by the platform clipboard listener. It returns
// whether the clip was sent.
func (s *Sync) LocalChanged(c Clip) (bool, error) {
	if c.Empty() {
		return false, nil
	}
	h := c.Hash()
	s.mu.Lock()
	if !s.canSend() || h == s.lastApplied || h == s.lastSent {
		s.mu.Unlock()
		return false, nil
	}
	s.seq++
	seq, epoch := s.seq, s.epoch
	s.lastSent = h
	s.mu.Unlock()

	if len(c.Files) > 0 {
		s.audit("clipboard.files_offered", map[string]any{"count": len(c.Files)})
		return true, s.peer.Send(protocol.ClipFiles{T: protocol.TClipFiles, Epoch: epoch, Paths: c.Files}, nil)
	}
	formats := []protocol.ClipFormat{}
	payload := []byte{}
	add := func(f string, b []byte) {
		if len(b) > 0 {
			formats = append(formats, protocol.ClipFormat{F: f, Len: len(b)})
			payload = append(payload, b...)
		}
	}
	add("text", []byte(c.Text))
	add("rtf", c.RTF)
	add("html", c.HTML)
	if len(payload)+len(c.PNG) <= MaxBytes {
		add("png", c.PNG)
	}
	if len(payload) > MaxBytes {
		s.audit("clipboard.too_large", map[string]any{"bytes": len(payload)})
		return false, nil
	}
	if len(formats) == 0 {
		return false, nil
	}
	s.audit("clipboard.sent", map[string]any{"formats": formatNames(formats), "bytes": len(payload)})
	return true, s.peer.Send(protocol.ClipData{T: protocol.TClipData, Epoch: epoch, Seq: seq, Hash: h, Formats: formats}, payload)
}

func formatNames(f []protocol.ClipFormat) []string {
	out := make([]string, len(f))
	for i, x := range f {
		out[i] = x.F
	}
	return out
}

func (s *Sync) audit(ev string, f map[string]any) {
	if s.Audit != nil {
		s.Audit(ev, f)
	}
}

// Handle processes a clip.* message from the peer.
func (s *Sync) Handle(m *protocol.Message) error {
	switch m.Type {
	case protocol.TClipConfig:
		if s.isViewer {
			return nil // only the technician sets the configuration
		}
		var c protocol.ClipConfig
		if err := m.Into(&c); err != nil {
			return err
		}
		return s.Configure(c.Enabled, c.Direction)
	case protocol.TClipFiles:
		var f protocol.ClipFiles
		if err := m.Into(&f); err != nil {
			return err
		}
		s.mu.Lock()
		ok := s.canReceive() && f.Epoch == s.epoch
		s.mu.Unlock()
		if ok && s.OnFiles != nil && len(f.Paths) > 0 {
			s.OnFiles(f.Paths)
		}
		return nil
	case protocol.TClipData:
		var d protocol.ClipData
		if err := m.Into(&d); err != nil {
			return err
		}
		s.mu.Lock()
		if !s.canReceive() || d.Epoch != s.epoch {
			s.mu.Unlock()
			s.audit("clipboard.dropped", map[string]any{"reason": "disabled, wrong direction or stale"})
			return nil
		}
		s.mu.Unlock()
		var c Clip
		off := 0
		for _, f := range d.Formats {
			if f.Len < 0 || off+f.Len > len(m.Payload) {
				return errors.New("clipsync: bad payload")
			}
			b := m.Payload[off : off+f.Len]
			off += f.Len
			switch f.F {
			case "text":
				c.Text = string(b)
			case "rtf":
				c.RTF = append([]byte(nil), b...)
			case "html":
				c.HTML = append([]byte(nil), b...)
			case "png":
				c.PNG = append([]byte(nil), b...)
			}
		}
		if c.Hash() != d.Hash {
			return errors.New("clipsync: hash mismatch")
		}
		s.mu.Lock()
		s.lastApplied = d.Hash
		s.mu.Unlock()
		s.audit("clipboard.received", map[string]any{"formats": formatNames(d.Formats), "bytes": len(m.Payload)})
		if s.Apply != nil {
			return s.Apply(c)
		}
	}
	return nil
}
