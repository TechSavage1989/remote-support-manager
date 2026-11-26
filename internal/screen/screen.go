// Package screen turns captured frames into changed JPEG tiles and back.
package screen

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"sync/atomic"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

// TileSize is the edge of a square tile in pixels.
const TileSize = 64

// MaxDimension rejects absurd frames from a misbehaving peer.
const MaxDimension = 16384

// Encoder remembers the previous frame and emits only changed tiles.
// Reset may be called from any goroutine; Encode from one goroutine.
type Encoder struct {
	Quality int
	prev    *image.RGBA
	seq     int64
	reset   atomic.Bool
}

// Encode compares img with the previous frame and returns a scr.frame header
// and payload. ok is false when nothing changed.
func (e *Encoder) Encode(img *image.RGBA, monitor int, full bool) (hdr protocol.Frame, payload []byte, ok bool, err error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if e.reset.Swap(false) {
		e.prev = nil
	}
	if e.prev == nil || e.prev.Bounds().Dx() != w || e.prev.Bounds().Dy() != h {
		full = true
		e.prev = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	q := e.Quality
	if q <= 0 {
		q = 70
	}
	var buf bytes.Buffer
	tiles := []protocol.Tile{}
	for ty := 0; ty < h; ty += TileSize {
		for tx := 0; tx < w; tx += TileSize {
			r := image.Rect(tx, ty, min(tx+TileSize, w), min(ty+TileSize, h))
			if !full && same(img, e.prev, r) {
				continue
			}
			sub := img.SubImage(r.Add(b.Min))
			start := buf.Len()
			if err := jpeg.Encode(&buf, sub, &jpeg.Options{Quality: q}); err != nil {
				return hdr, nil, false, err
			}
			tiles = append(tiles, protocol.Tile{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy(), Len: buf.Len() - start})
			draw.Draw(e.prev, r, img, r.Min.Add(b.Min), draw.Src)
		}
	}
	if len(tiles) == 0 {
		return hdr, nil, false, nil
	}
	e.seq++
	return protocol.Frame{T: protocol.TScrFrame, Seq: e.seq, Monitor: monitor, W: w, H: h, Full: full, Tiles: tiles}, buf.Bytes(), true, nil
}

// Reset forces the next frame to be sent in full.
func (e *Encoder) Reset() { e.reset.Store(true) }

func same(a, b *image.RGBA, r image.Rectangle) bool {
	ab := a.Bounds().Min
	for y := r.Min.Y; y < r.Max.Y; y++ {
		ao := a.PixOffset(r.Min.X+ab.X, y+ab.Y)
		bo := b.PixOffset(r.Min.X, y)
		n := r.Dx() * 4
		if !bytes.Equal(a.Pix[ao:ao+n], b.Pix[bo:bo+n]) {
			return false
		}
	}
	return true
}

// Canvas is the viewer's copy of the remote screen.
type Canvas struct {
	Img     *image.RGBA
	Monitor int
}

// Apply decodes the tiles of a frame onto the canvas and returns the dirty
// rectangle.
func (c *Canvas) Apply(f protocol.Frame, payload []byte) (image.Rectangle, error) {
	if f.W <= 0 || f.H <= 0 || f.W > MaxDimension || f.H > MaxDimension {
		return image.Rectangle{}, errors.New("screen: bad frame size")
	}
	if c.Img == nil || c.Img.Bounds().Dx() != f.W || c.Img.Bounds().Dy() != f.H || c.Monitor != f.Monitor {
		c.Img = image.NewRGBA(image.Rect(0, 0, f.W, f.H))
		c.Monitor = f.Monitor
	}
	dirty := image.Rectangle{}
	off := 0
	for _, t := range f.Tiles {
		if t.Len <= 0 || off+t.Len > len(payload) {
			return dirty, errors.New("screen: bad tile length")
		}
		r := image.Rect(t.X, t.Y, t.X+t.W, t.Y+t.H)
		if !r.In(c.Img.Bounds()) {
			return dirty, errors.New("screen: tile outside frame")
		}
		img, err := jpeg.Decode(bytes.NewReader(payload[off : off+t.Len]))
		off += t.Len
		if err != nil {
			return dirty, err
		}
		draw.Draw(c.Img, r, img, img.Bounds().Min, draw.Src)
		dirty = dirty.Union(r)
	}
	return dirty, nil
}

// Fit returns the largest rectangle with the frame's aspect ratio that fits
// in a viewport of vw x vh, centred.
func Fit(fw, fh, vw, vh int) image.Rectangle {
	if fw <= 0 || fh <= 0 || vw <= 0 || vh <= 0 {
		return image.Rectangle{}
	}
	w, h := vw, fh*vw/fw
	if h > vh {
		h, w = vh, fw*vh/fh
	}
	x, y := (vw-w)/2, (vh-h)/2
	return image.Rect(x, y, x+w, y+h)
}

// ToRemote maps a viewport point back to remote pixel coordinates for the
// given displayed rectangle. ok is false outside the picture.
func ToRemote(px, py int, shown image.Rectangle, fw, fh int) (x, y int, ok bool) {
	if !image.Pt(px, py).In(shown) || shown.Dx() == 0 || shown.Dy() == 0 {
		return 0, 0, false
	}
	x = (px - shown.Min.X) * fw / shown.Dx()
	y = (py - shown.Min.Y) * fh / shown.Dy()
	return min(x, fw-1), min(y, fh-1), true
}
