//go:build windows

package winapi

import (
	"errors"
	"image"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/protocol"
)

type monitorInfoEx struct {
	Size    uint32
	Monitor RECT
	Work    RECT
	Flags   uint32
	Device  [32]uint16
}

// Monitors lists displays in virtual-desktop pixels (requires DPI awareness).
func Monitors() []protocol.Monitor {
	var out []protocol.Monitor
	cb := syscall.NewCallback(func(hmon, hdc, rect, data uintptr) uintptr {
		mi := monitorInfoEx{}
		mi.Size = uint32(unsafe.Sizeof(mi))
		call(pGetMonitorInfoW, hmon, uintptr(unsafe.Pointer(&mi)))
		r := mi.Monitor
		out = append(out, protocol.Monitor{
			Index: len(out), Name: windows.UTF16ToString(mi.Device[:]),
			X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top), Primary: mi.Flags&1 != 0,
		})
		return 1
	})
	call(pEnumDisplayMonitors, 0, 0, cb, 0)
	// Put the primary display first so index 0 is what users expect.
	for i, m := range out {
		if m.Primary && i != 0 {
			out[0], out[i] = out[i], out[0]
		}
	}
	for i := range out {
		out[i].Index = i
	}
	return out
}

// Capturer grabs a monitor with GDI BitBlt into a reusable DIB section.
// It sees the interactive user's desktop; the secure desktop (UAC prompts,
// lock screen) is not visible to a user-level process.
type Capturer struct {
	w, h  int32
	memDC uintptr
	bmp   uintptr
	bits  unsafe.Pointer
	old   uintptr
}

func (c *Capturer) ensure(w, h int32) error {
	if c.memDC != 0 && c.w == w && c.h == h {
		return nil
	}
	c.Close()
	screen := call(pGetDC, 0)
	defer call(pReleaseDC, 0, screen)
	c.memDC = call(pCreateCompatibleDC, screen)
	bi := BITMAPINFOHEADER{Width: w, Height: -h, Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	c.bmp = call(pCreateDIBSection, screen, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if c.bmp == 0 || bits == nil {
		return errors.New("CreateDIBSection failed")
	}
	c.bits = bits
	c.old = call(pSelectObject, c.memDC, c.bmp)
	c.w, c.h = w, h
	return nil
}

// Close frees GDI objects.
func (c *Capturer) Close() {
	if c.memDC != 0 {
		call(pSelectObject, c.memDC, c.old)
		call(pDeleteObject, c.bmp)
		call(pDeleteDC, c.memDC)
		c.memDC, c.bmp, c.bits = 0, 0, nil
	}
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	Pos    POINT
}

type iconInfo struct {
	Icon     int32
	XHotspot uint32
	YHotspot uint32
	Mask     uintptr
	Color    uintptr
}

// Capture returns the monitor as RGBA, including the mouse pointer.
func (c *Capturer) Capture(m protocol.Monitor) (*image.RGBA, error) {
	if m.W <= 0 || m.H <= 0 {
		return nil, errors.New("no monitor")
	}
	if err := c.ensure(int32(m.W), int32(m.H)); err != nil {
		return nil, err
	}
	screen := call(pGetDC, 0)
	const srccopy, captureblt = 0x00CC0020, 0x40000000
	ok := call(pBitBlt, c.memDC, 0, 0, uintptr(m.W), uintptr(m.H), screen, uintptr(m.X), uintptr(m.Y), srccopy|captureblt)
	call(pReleaseDC, 0, screen)
	if ok == 0 {
		return nil, errors.New("BitBlt failed (desktop may be locked or on the secure desktop)")
	}
	ci := cursorInfo{}
	ci.Size = uint32(unsafe.Sizeof(ci))
	if call(pGetCursorInfo, uintptr(unsafe.Pointer(&ci))) != 0 && ci.Flags&1 != 0 {
		var ii iconInfo
		if call(pGetIconInfo, ci.Cursor, uintptr(unsafe.Pointer(&ii))) != 0 {
			x := int32(ci.Pos.X) - int32(m.X) - int32(ii.XHotspot)
			y := int32(ci.Pos.Y) - int32(m.Y) - int32(ii.YHotspot)
			call(pDrawIconEx, c.memDC, uintptr(x), uintptr(y), ci.Cursor, 0, 0, 0, 0, 3)
			if ii.Mask != 0 {
				call(pDeleteObject, ii.Mask)
			}
			if ii.Color != 0 {
				call(pDeleteObject, ii.Color)
			}
		}
	}
	n := m.W * m.H * 4
	src := unsafe.Slice((*byte)(c.bits), n)
	img := image.NewRGBA(image.Rect(0, 0, m.W, m.H))
	dst := img.Pix
	for i := 0; i < n; i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 255
	}
	return img, nil
}

// RGBAtoBGRA converts for drawing with DrawBGRA.
func RGBAtoBGRA(src []byte, dst []byte) []byte {
	if cap(dst) < len(src) {
		dst = make([]byte, len(src))
	}
	dst = dst[:len(src)]
	for i := 0; i+3 < len(src); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0
	}
	return dst
}
