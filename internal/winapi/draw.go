//go:build windows

package winapi

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pDrawTextW     = user32.NewProc("DrawTextW")
	pRoundRect     = gdi32.NewProc("RoundRect")
	pCreatePen     = gdi32.NewProc("CreatePen")
	pGetSysColor   = user32.NewProc("GetSysColor")
	pFillRectU     = user32.NewProc("FillRect")
	pGetTextExtent = gdi32.NewProc("GetTextExtentPoint32W")

	fontMu    sync.Mutex
	fontCache = map[[2]int]uintptr{}
)

// DrawText flags.
const (
	DT_LEFT         = 0x0
	DT_CENTER       = 0x1
	DT_RIGHT        = 0x2
	DT_VCENTER      = 0x4
	DT_WORDBREAK    = 0x10
	DT_SINGLELINE   = 0x20
	DT_NOPREFIX     = 0x800
	DT_CALCRECT     = 0x400
	DT_END_ELLIPSIS = 0x8000
)

// Font returns a cached Segoe UI font of px height and weight (400 normal,
// 600 semibold).
func Font(px, weight int) uintptr {
	fontMu.Lock()
	defer fontMu.Unlock()
	k := [2]int{px, weight}
	if f, ok := fontCache[k]; ok {
		return f
	}
	f := call(pCreateFontW, ^uintptr(px-1), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(W("Segoe UI"))))
	fontCache[k] = f
	return f
}

// Select selects a GDI object into dc and returns the previous one.
func Select(dc windows.Handle, obj uintptr) uintptr { return call(pSelectObject, uintptr(dc), obj) }

// DeleteObject frees a GDI object.
func DeleteObject(obj uintptr) { call(pDeleteObject, obj) }

// Text draws s in r with the given font, colour and DT_ flags and returns
// the height used.
func Text(dc windows.Handle, s string, r RECT, font uintptr, rgb uint32, flags uint32) int32 {
	old := Select(dc, font)
	TransparentText(uintptr(dc), rgb)
	p := windows.StringToUTF16(s)
	h := call(pDrawTextW, uintptr(dc), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), uintptr(flags|DT_NOPREFIX))
	Select(dc, old)
	return int32(h)
}

// MeasureText returns the height s needs when wrapped to width w.
func MeasureText(dc windows.Handle, s string, w int32, font uintptr, flags uint32) int32 {
	r := RECT{Right: w}
	old := Select(dc, font)
	p := windows.StringToUTF16(s)
	call(pDrawTextW, uintptr(dc), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), uintptr(flags|DT_CALCRECT|DT_NOPREFIX))
	Select(dc, old)
	return r.Bottom
}

// TextWidth returns the width of a single line of text.
func TextWidth(dc windows.Handle, s string, font uintptr) int32 {
	if s == "" {
		return 0
	}
	old := Select(dc, font)
	p := windows.StringToUTF16(s)
	var sz struct{ CX, CY int32 }
	call(pGetTextExtent, uintptr(dc), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&sz)))
	Select(dc, old)
	return sz.CX
}

// Rounded draws a rounded rectangle filled with fill and outlined with line
// (pass the same colour for no visible outline).
func Rounded(dc windows.Handle, r RECT, radius int32, fill, line uint32) {
	brush := SolidBrush(fill)
	pen := call(pCreatePen, 0, 1, uintptr(line))
	ob := Select(dc, brush)
	op := Select(dc, pen)
	call(pRoundRect, uintptr(dc), uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(radius), uintptr(radius))
	Select(dc, ob)
	Select(dc, op)
	DeleteObject(brush)
	DeleteObject(pen)
}

// Fill fills r with a solid colour.
func Fill(dc windows.Handle, r RECT, rgb uint32) {
	b := SolidBrush(rgb)
	call(pFillRectU, uintptr(dc), uintptr(unsafe.Pointer(&r)), b)
	DeleteObject(b)
}

// SysColor returns a system colour (0x00BBGGRR).
func SysColor(i int) uint32 { return uint32(call(pGetSysColor, uintptr(i))) }

// WindowDC returns a DC for measuring outside WM_PAINT; release it with
// ReleaseWindowDC.
func WindowDC(h windows.HWND) windows.Handle { return windows.Handle(call(pGetDC, uintptr(h))) }

// ReleaseWindowDC releases a DC from WindowDC.
func ReleaseWindowDC(h windows.HWND, dc windows.Handle) { call(pReleaseDC, uintptr(h), uintptr(dc)) }

// RGB builds a COLORREF.
func RGB(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

// DarkMode reports whether Windows apps are set to the dark theme.
func DarkMode() bool {
	v, err := readDWORD(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, "AppsUseLightTheme")
	return err == nil && v == 0
}

var pShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// OpenURL opens an https link in the default browser. Other schemes are
// ignored so a crafted value can never launch a program.
func OpenURL(u string) {
	if len(u) < 8 || u[:8] != "https://" {
		return
	}
	call(pShellExecuteW, 0, uintptr(unsafe.Pointer(W("open"))), uintptr(unsafe.Pointer(W(u))), 0, 0, 1)
}
