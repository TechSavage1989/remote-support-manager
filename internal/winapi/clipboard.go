//go:build windows

package winapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/clipsync"
)

const (
	cfUnicodeText = 13
	cfDIB         = 8
	cfHDROP       = 15
	gmemMoveable  = 0x0002
)

var (
	cfRTF  = uintptr(0)
	cfHTML = uintptr(0)
	cfPNG  = uintptr(0)
)

func formats() {
	if cfRTF == 0 {
		cfRTF = call(pRegisterClipboardFormatW, uintptr(unsafe.Pointer(W("Rich Text Format"))))
		cfHTML = call(pRegisterClipboardFormatW, uintptr(unsafe.Pointer(W("HTML Format"))))
		cfPNG = call(pRegisterClipboardFormatW, uintptr(unsafe.Pointer(W("PNG"))))
	}
}

func openClipboard(owner windows.HWND) error {
	for i := 0; i < 10; i++ {
		if call(pOpenClipboard, uintptr(owner)) != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond) // another app holds it briefly
	}
	return errors.New("clipboard is busy")
}

func globalBytes(h uintptr) []byte {
	if h == 0 {
		return nil
	}
	p := call(pGlobalLock, h)
	if p == 0 {
		return nil
	}
	defer call(pGlobalUnlock, h)
	n := call(pGlobalSize, h)
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(p)), n)...)
}

func setGlobal(format uintptr, b []byte) {
	h := call(pGlobalAlloc, gmemMoveable, uintptr(len(b)))
	if h == 0 {
		return
	}
	p := call(pGlobalLock, h)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(b)), b)
	call(pGlobalUnlock, h)
	if call(pSetClipboardData, format, h) == 0 {
		call(pGlobalFree, h)
	}
}

func available(f uintptr) bool { return call(pIsClipboardFormatAvailable, f) != 0 }

// ReadClipboard returns text, rich text, HTML, an image (as PNG) and copied
// file paths. Contents are never logged.
func ReadClipboard(owner windows.HWND) (clipsync.Clip, error) {
	formats()
	var c clipsync.Clip
	if err := openClipboard(owner); err != nil {
		return c, err
	}
	defer call(pCloseClipboard)
	if available(cfHDROP) {
		h := call(pGetClipboardData, cfHDROP)
		n := call(pDragQueryFileW, h, 0xFFFFFFFF, 0, 0)
		for i := uintptr(0); i < n && i < 1000; i++ {
			l := call(pDragQueryFileW, h, i, 0, 0)
			buf := make([]uint16, l+1)
			call(pDragQueryFileW, h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
			c.Files = append(c.Files, windows.UTF16ToString(buf))
		}
		return c, nil
	}
	if available(cfUnicodeText) {
		b := globalBytes(call(pGetClipboardData, cfUnicodeText))
		u := unsafe.Slice((*uint16)(unsafe.Pointer(unsafe.SliceData(b))), len(b)/2)
		c.Text = windows.UTF16ToString(u)
	}
	if available(cfRTF) {
		c.RTF = trimNul(globalBytes(call(pGetClipboardData, cfRTF)))
	}
	if available(cfHTML) {
		c.HTML = trimNul(globalBytes(call(pGetClipboardData, cfHTML)))
	}
	if available(cfPNG) {
		c.PNG = globalBytes(call(pGetClipboardData, cfPNG))
	} else if available(cfDIB) {
		if p, err := dibToPNG(globalBytes(call(pGetClipboardData, cfDIB))); err == nil {
			c.PNG = p
		}
	}
	return c, nil
}

func trimNul(b []byte) []byte {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return b[:i]
	}
	return b
}

// WriteClipboard replaces the clipboard with c (not files).
func WriteClipboard(owner windows.HWND, c clipsync.Clip) error {
	formats()
	if err := openClipboard(owner); err != nil {
		return err
	}
	defer call(pCloseClipboard)
	call(pEmptyClipboard)
	if c.Text != "" {
		u, _ := windows.UTF16FromString(c.Text)
		setGlobal(cfUnicodeText, unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2))
	}
	if len(c.RTF) > 0 {
		setGlobal(cfRTF, append(append([]byte(nil), c.RTF...), 0))
	}
	if len(c.HTML) > 0 {
		setGlobal(cfHTML, append(append([]byte(nil), c.HTML...), 0))
	}
	if len(c.PNG) > 0 {
		setGlobal(cfPNG, c.PNG)
		if dib, err := pngToDIB(c.PNG); err == nil {
			setGlobal(cfDIB, dib)
		}
	}
	return nil
}

// ClipboardListener registers hwnd for WM_CLIPBOARDUPDATE.
func ClipboardListener(hwnd windows.HWND, on bool) {
	if on {
		call(pAddClipboardFormatListener, uintptr(hwnd))
	} else {
		call(pRemoveClipboardFormatListen, uintptr(hwnd))
	}
}

// dibToPNG converts a packed CF_DIB (24 or 32 bpp, uncompressed or
// BI_BITFIELDS) to PNG.
func dibToPNG(b []byte) ([]byte, error) {
	if len(b) < 40 {
		return nil, errors.New("short DIB")
	}
	hs := binary.LittleEndian.Uint32(b[0:])
	w := int(int32(binary.LittleEndian.Uint32(b[4:])))
	h := int(int32(binary.LittleEndian.Uint32(b[8:])))
	bpp := int(binary.LittleEndian.Uint16(b[14:]))
	comp := binary.LittleEndian.Uint32(b[16:])
	if w <= 0 || w > 16384 || h == 0 || h > 16384 || h < -16384 || (bpp != 24 && bpp != 32) || (comp != 0 && comp != 3) {
		return nil, errors.New("unsupported DIB")
	}
	off := int(hs)
	if comp == 3 && hs == 40 {
		off += 12
	}
	topDown := h < 0
	if topDown {
		h = -h
	}
	stride := ((w*bpp + 31) / 32) * 4
	if off+stride*h > len(b) {
		return nil, errors.New("truncated DIB")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	alpha := false
	for y := 0; y < h; y++ {
		sy := h - 1 - y
		if topDown {
			sy = y
		}
		row := b[off+sy*stride:]
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			if bpp == 32 {
				p := row[x*4:]
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = p[2], p[1], p[0], p[3]
				if p[3] != 0 {
					alpha = true
				}
			} else {
				p := row[x*3:]
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = p[2], p[1], p[0], 255
			}
		}
	}
	if bpp == 32 && !alpha { // most apps leave alpha at 0 meaning opaque
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	var out bytes.Buffer
	err := png.Encode(&out, img)
	return out.Bytes(), err
}

// pngToDIB converts PNG to a 32 bpp bottom-up CF_DIB.
func pngToDIB(p []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(p))
	if err != nil {
		return nil, err
	}
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	out := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(out[0:], 40)
	binary.LittleEndian.PutUint32(out[4:], uint32(w))
	binary.LittleEndian.PutUint32(out[8:], uint32(h))
	binary.LittleEndian.PutUint16(out[12:], 1)
	binary.LittleEndian.PutUint16(out[14:], 32)
	binary.LittleEndian.PutUint32(out[20:], uint32(w*h*4))
	for y := 0; y < h; y++ {
		row := out[40+(h-1-y)*w*4:]
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA()
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = byte(b>>8), byte(g>>8), byte(r>>8), byte(a>>8)
		}
	}
	return out, nil
}
