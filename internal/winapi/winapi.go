//go:build windows

// Package winapi holds the small set of Win32 calls the agent and viewer
// use: GDI capture, SendInput, the clipboard, simple windows and the
// message loop. Only documented, supported APIs are used.
package winapi

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	sasDLL   = windows.NewLazySystemDLL("sas.dll")

	pGetSystemMetrics            = user32.NewProc("GetSystemMetrics")
	pEnumDisplayMonitors         = user32.NewProc("EnumDisplayMonitors")
	pGetMonitorInfoW             = user32.NewProc("GetMonitorInfoW")
	pGetDC                       = user32.NewProc("GetDC")
	pReleaseDC                   = user32.NewProc("ReleaseDC")
	pSendInput                   = user32.NewProc("SendInput")
	pOpenClipboard               = user32.NewProc("OpenClipboard")
	pCloseClipboard              = user32.NewProc("CloseClipboard")
	pEmptyClipboard              = user32.NewProc("EmptyClipboard")
	pGetClipboardData            = user32.NewProc("GetClipboardData")
	pSetClipboardData            = user32.NewProc("SetClipboardData")
	pRegisterClipboardFormatW    = user32.NewProc("RegisterClipboardFormatW")
	pIsClipboardFormatAvailable  = user32.NewProc("IsClipboardFormatAvailable")
	pAddClipboardFormatListener  = user32.NewProc("AddClipboardFormatListener")
	pRemoveClipboardFormatListen = user32.NewProc("RemoveClipboardFormatListener")
	pRegisterClassExW            = user32.NewProc("RegisterClassExW")
	pCreateWindowExW             = user32.NewProc("CreateWindowExW")
	pDefWindowProcW              = user32.NewProc("DefWindowProcW")
	pDestroyWindow               = user32.NewProc("DestroyWindow")
	pGetMessageW                 = user32.NewProc("GetMessageW")
	pTranslateMessage            = user32.NewProc("TranslateMessage")
	pDispatchMessageW            = user32.NewProc("DispatchMessageW")
	pIsDialogMessageW            = user32.NewProc("IsDialogMessageW")
	pPostMessageW                = user32.NewProc("PostMessageW")
	pSendMessageW                = user32.NewProc("SendMessageW")
	pPostQuitMessage             = user32.NewProc("PostQuitMessage")
	pShowWindow                  = user32.NewProc("ShowWindow")
	pUpdateWindow                = user32.NewProc("UpdateWindow")
	pSetWindowPos                = user32.NewProc("SetWindowPos")
	pMoveWindow                  = user32.NewProc("MoveWindow")
	pGetClientRect               = user32.NewProc("GetClientRect")
	pGetWindowRect               = user32.NewProc("GetWindowRect")
	pInvalidateRect              = user32.NewProc("InvalidateRect")
	pBeginPaint                  = user32.NewProc("BeginPaint")
	pEndPaint                    = user32.NewProc("EndPaint")
	pFillRect                    = user32.NewProc("FillRect")
	pSetWindowTextW              = user32.NewProc("SetWindowTextW")
	pGetWindowTextW              = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW        = user32.NewProc("GetWindowTextLengthW")
	pEnableWindow                = user32.NewProc("EnableWindow")
	pSetFocus                    = user32.NewProc("SetFocus")
	pGetFocus                    = user32.NewProc("GetFocus")
	pSetForegroundWindow         = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow         = user32.NewProc("GetForegroundWindow")
	pMessageBoxW                 = user32.NewProc("MessageBoxW")
	pLoadCursorW                 = user32.NewProc("LoadCursorW")
	pLoadIconW                   = user32.NewProc("LoadIconW")
	pSetTimer                    = user32.NewProc("SetTimer")
	pKillTimer                   = user32.NewProc("KillTimer")
	pSetWindowsHookExW           = user32.NewProc("SetWindowsHookExW")
	pUnhookWindowsHookEx         = user32.NewProc("UnhookWindowsHookEx")
	pCallNextHookEx              = user32.NewProc("CallNextHookEx")
	pCreateMenu                  = user32.NewProc("CreateMenu")
	pCreatePopupMenu             = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                 = user32.NewProc("AppendMenuW")
	pSetMenu                     = user32.NewProc("SetMenu")
	pCheckMenuItem               = user32.NewProc("CheckMenuItem")
	pEnableMenuItem              = user32.NewProc("EnableMenuItem")
	pDeleteMenu                  = user32.NewProc("DeleteMenu")
	pGetMenuItemCount            = user32.NewProc("GetMenuItemCount")
	pDrawMenuBar                 = user32.NewProc("DrawMenuBar")
	pTrackPopupMenu              = user32.NewProc("TrackPopupMenu")
	pGetCursorPos                = user32.NewProc("GetCursorPos")
	pGetCursorInfo               = user32.NewProc("GetCursorInfo")
	pGetIconInfo                 = user32.NewProc("GetIconInfo")
	pDrawIconEx                  = user32.NewProc("DrawIconEx")
	pSetWindowLongPtrW           = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtrW           = user32.NewProc("GetWindowLongPtrW")
	pSetScrollInfo               = user32.NewProc("SetScrollInfo")
	pGetScrollInfo               = user32.NewProc("GetScrollInfo")
	pSetCapture                  = user32.NewProc("SetCapture")
	pReleaseCapture              = user32.NewProc("ReleaseCapture")
	pSetProcessDpiAwarenessCtx   = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForWindow             = user32.NewProc("GetDpiForWindow")
	pMonitorFromWindow           = user32.NewProc("MonitorFromWindow")
	pIsWindow                    = user32.NewProc("IsWindow")
	pGetKeyState                 = user32.NewProc("GetKeyState")
	pMapVirtualKeyW              = user32.NewProc("MapVirtualKeyW")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pBitBlt             = gdi32.NewProc("BitBlt")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pStretchDIBits      = gdi32.NewProc("StretchDIBits")
	pSetStretchBltMode  = gdi32.NewProc("SetStretchBltMode")
	pCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	pGetStockObject     = gdi32.NewProc("GetStockObject")
	pCreateFontW        = gdi32.NewProc("CreateFontW")

	pGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	pGlobalLock   = kernel32.NewProc("GlobalLock")
	pGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	pGlobalSize   = kernel32.NewProc("GlobalSize")
	pGlobalFree   = kernel32.NewProc("GlobalFree")
	pGetTick64    = kernel32.NewProc("GetTickCount64")
	pGetDiskFree  = kernel32.NewProc("GetDiskFreeSpaceExW")
	pGetDrives    = kernel32.NewProc("GetLogicalDriveStringsW")
	pGetDriveType = kernel32.NewProc("GetDriveTypeW")
	pGetModuleH   = kernel32.NewProc("GetModuleHandleW")

	pDragQueryFileW    = shell32.NewProc("DragQueryFileW")
	pDragAcceptFiles   = shell32.NewProc("DragAcceptFiles")
	pDragFinish        = shell32.NewProc("DragFinish")
	pShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	pInitCommonCtrlsEx = comctl32.NewProc("InitCommonControlsEx")
	pSendSAS           = sasDLL.NewProc("SendSAS")
)

// Window messages and constants used across the app.
const (
	WM_CREATE          = 0x0001
	WM_DESTROY         = 0x0002
	WM_SIZE            = 0x0005
	WM_ACTIVATE        = 0x0006
	WM_SETFOCUS        = 0x0007
	WM_KILLFOCUS       = 0x0008
	WM_PAINT           = 0x000F
	WM_CLOSE           = 0x0010
	WM_ERASEBKGND      = 0x0014
	WM_SETFONT         = 0x0030
	WM_GETMINMAXINFO   = 0x0024
	WM_NOTIFY          = 0x004E
	WM_KEYDOWN         = 0x0100
	WM_KEYUP           = 0x0101
	WM_CHAR            = 0x0102
	WM_SYSKEYDOWN      = 0x0104
	WM_SYSKEYUP        = 0x0105
	WM_SYSCHAR         = 0x0106
	WM_COMMAND         = 0x0111
	WM_SYSCOMMAND      = 0x0112
	WM_TIMER           = 0x0113
	WM_HSCROLL         = 0x0114
	WM_VSCROLL         = 0x0115
	WM_MOUSEMOVE       = 0x0200
	WM_LBUTTONDOWN     = 0x0201
	WM_LBUTTONUP       = 0x0202
	WM_LBUTTONDBLCLK   = 0x0203
	WM_RBUTTONDOWN     = 0x0204
	WM_RBUTTONUP       = 0x0205
	WM_RBUTTONDBLCLK   = 0x0206
	WM_MBUTTONDOWN     = 0x0207
	WM_MBUTTONUP       = 0x0208
	WM_MOUSEWHEEL      = 0x020A
	WM_MOUSEHWHEEL     = 0x020E
	WM_DROPFILES       = 0x0233
	WM_CLIPBOARDUPDATE = 0x031D
	WM_APP             = 0x8000
	WM_USER            = 0x0400

	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_POPUP            = 0x80000000
	WS_CHILD            = 0x40000000
	WS_VISIBLE          = 0x10000000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	WS_BORDER           = 0x00800000
	WS_TABSTOP          = 0x00010000
	WS_VSCROLL          = 0x00200000
	WS_HSCROLL          = 0x00100000
	WS_CLIPCHILDREN     = 0x02000000
	WS_DISABLED         = 0x08000000
	WS_MINIMIZEBOX      = 0x00020000
	WS_EX_TOPMOST       = 0x00000008
	WS_EX_TOOLWINDOW    = 0x00000080
	WS_EX_CLIENTEDGE    = 0x00000200
	WS_EX_ACCEPTFILES   = 0x00000010
	WS_EX_DLGMODALFRAME = 0x00000001
	WS_EX_NOACTIVATE    = 0x08000000

	BS_PUSHBUTTON    = 0x0
	BS_DEFPUSHBUTTON = 0x1
	BS_AUTOCHECKBOX  = 0x3
	ES_AUTOHSCROLL   = 0x80
	ES_NUMBER        = 0x2000
	ES_MULTILINE     = 0x4
	ES_READONLY      = 0x800
	ES_AUTOVSCROLL   = 0x40
	SS_LEFT          = 0x0
	SS_CENTER        = 0x1
	BM_GETCHECK      = 0x00F0
	BM_SETCHECK      = 0x00F1
	EM_SETLIMITTEXT  = 0x00C5
	BN_CLICKED       = 0

	SW_HIDE     = 0
	SW_SHOW     = 5
	SW_MAXIMIZE = 3
	SW_RESTORE  = 9

	CW_USEDEFAULT = 0x80000000

	MB_OK              = 0x0
	MB_OKCANCEL        = 0x1
	MB_YESNO           = 0x4
	MB_ICONINFORMATION = 0x40
	MB_ICONWARNING     = 0x30
	MB_ICONERROR       = 0x10
	MB_ICONQUESTION    = 0x20
	MB_TOPMOST         = 0x40000
	MB_SETFOREGROUND   = 0x10000
	IDOK               = 1
	IDCANCEL           = 2
	IDYES              = 6

	MF_STRING     = 0x0
	MF_POPUP      = 0x10
	MF_SEPARATOR  = 0x800
	MF_CHECKED    = 0x8
	MF_UNCHECKED  = 0x0
	MF_GRAYED     = 0x1
	MF_ENABLED    = 0x0
	MF_BYCOMMAND  = 0x0
	MF_BYPOSITION = 0x400
	TPM_RETURNCMD = 0x100

	SWP_NOSIZE       = 0x1
	SWP_NOMOVE       = 0x2
	SWP_NOZORDER     = 0x4
	SWP_SHOWWINDOW   = 0x40
	SWP_FRAMECHANGED = 0x20
	HWND_TOPMOST     = ^uintptr(0) // -1
	HWND_NOTOPMOST   = ^uintptr(1) // -2

	SM_CXSCREEN        = 0
	SM_CYSCREEN        = 1
	SM_XVIRTUALSCREEN  = 76
	SM_YVIRTUALSCREEN  = 77
	SM_CXVIRTUALSCREEN = 78
	SM_CYVIRTUALSCREEN = 79

	IDC_ARROW       = 32512
	IDI_APPLICATION = 32512
	COLOR_WINDOW    = 5
	COLOR_BTNFACE   = 15

	SB_HORZ          = 0
	SB_VERT          = 1
	SIF_RANGE        = 0x1
	SIF_PAGE         = 0x2
	SIF_POS          = 0x4
	SIF_TRACKPOS     = 0x10
	SIF_ALL          = 0x17
	SB_LINEUP        = 0
	SB_LINEDOWN      = 1
	SB_PAGEUP        = 2
	SB_PAGEDOWN      = 3
	SB_THUMBTRACK    = 5
	SB_THUMBPOSITION = 4

	WA_INACTIVE = 0
	SC_KEYMENU  = 0xF100
)

// RECT, POINT, MSG and friends.
type RECT struct{ Left, Top, Right, Bottom int32 }
type POINT struct{ X, Y int32 }
type MSG struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
	Private uint32
}

type WNDCLASSEX struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type PAINTSTRUCT struct {
	Hdc         windows.Handle
	Erase       int32
	RcPaint     RECT
	Restore     int32
	IncUpdate   int32
	RGBReserved [32]byte
}

type SCROLLINFO struct {
	Size     uint32
	Mask     uint32
	Min      int32
	Max      int32
	Page     uint32
	Pos      int32
	TrackPos int32
}

// W converts a Go string to a UTF-16 pointer.
func W(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func call(p *windows.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

// Instance returns the module handle.
func Instance() windows.Handle { return windows.Handle(call(pGetModuleH, 0)) }

// EnableDPIAwareness makes captured and drawn pixels match real pixels.
func EnableDPIAwareness() {
	if pSetProcessDpiAwarenessCtx.Find() == nil {
		const perMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
		call(pSetProcessDpiAwarenessCtx, perMonitorV2)
	}
}

// InitCommonControls loads list views and other common controls.
func InitCommonControls() {
	type icc struct{ Size, ICC uint32 }
	c := icc{8, 0x1 | 0x4000 | 0x20} // listview, standard, progress
	call(pInitCommonCtrlsEx, uintptr(unsafe.Pointer(&c)))
}

// ------------------------------------------------------------ windows

var (
	procMu   sync.Mutex
	procs    = map[windows.HWND]func(hwnd windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool){}
	wndProcC = syscall.NewCallback(wndProc)
	classes  = map[string]bool{}
)

func wndProc(hwnd windows.HWND, msg uint32, wp, lp uintptr) uintptr {
	procMu.Lock()
	h := procs[hwnd]
	procMu.Unlock()
	if h != nil {
		if r, handled := h(hwnd, msg, wp, lp); handled {
			return r
		}
	}
	if msg == WM_DESTROY {
		procMu.Lock()
		delete(procs, hwnd)
		procMu.Unlock()
	}
	return DefWindowProc(hwnd, msg, wp, lp)
}

// pendingProc is attached during CreateWindowEx so WM_CREATE is seen.
var pendingProc func(hwnd windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool)

func bootstrapProc(hwnd windows.HWND, msg uint32, wp, lp uintptr) uintptr {
	procMu.Lock()
	if _, ok := procs[hwnd]; !ok && pendingProc != nil {
		procs[hwnd] = pendingProc
	}
	procMu.Unlock()
	return wndProc(hwnd, msg, wp, lp)
}

var bootstrapC = syscall.NewCallback(bootstrapProc)

// RegisterClass registers a window class once.
func RegisterClass(name string, bg uintptr) {
	if classes[name] {
		return
	}
	wc := WNDCLASSEX{
		WndProc:    bootstrapC,
		Instance:   Instance(),
		Cursor:     windows.Handle(call(pLoadCursorW, 0, IDC_ARROW)),
		Icon:       windows.Handle(call(pLoadIconW, uintptr(Instance()), 1)),
		Background: windows.Handle(bg),
		ClassName:  W(name),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if wc.Icon == 0 {
		wc.Icon = windows.Handle(call(pLoadIconW, 0, IDI_APPLICATION))
	}
	call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc)))
	classes[name] = true
}

// Handler processes a window message; return handled=false for default.
type Handler func(hwnd windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool)

// CreateWindow creates a top-level or child window with a Go handler.
func CreateWindow(exStyle uint32, class, title string, style uint32, x, y, w, h int32, parent windows.HWND, menu uintptr, handler Handler) windows.HWND {
	pendingProc = handler
	hwnd := windows.HWND(call(pCreateWindowExW, uintptr(exStyle), uintptr(unsafe.Pointer(W(class))), uintptr(unsafe.Pointer(W(title))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(parent), menu, uintptr(Instance()), 0))
	pendingProc = nil
	if handler != nil && hwnd != 0 {
		procMu.Lock()
		procs[hwnd] = handler
		procMu.Unlock()
	}
	return hwnd
}

// Control creates a standard child control (BUTTON, EDIT, STATIC, ...).
func Control(parent windows.HWND, class, text string, style uint32, x, y, w, h int32, id int) windows.HWND {
	hwnd := windows.HWND(call(pCreateWindowExW, 0, uintptr(unsafe.Pointer(W(class))), uintptr(unsafe.Pointer(W(text))),
		uintptr(WS_CHILD|WS_VISIBLE|style), uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(parent), uintptr(id), uintptr(Instance()), 0))
	SetFont(hwnd, DefaultFont())
	return hwnd
}

// ControlEx creates a child control with an extended style.
func ControlEx(parent windows.HWND, exStyle uint32, class, text string, style uint32, x, y, w, h int32, id int) windows.HWND {
	hwnd := windows.HWND(call(pCreateWindowExW, uintptr(exStyle), uintptr(unsafe.Pointer(W(class))), uintptr(unsafe.Pointer(W(text))),
		uintptr(WS_CHILD|WS_VISIBLE|style), uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(parent), uintptr(id), uintptr(Instance()), 0))
	SetFont(hwnd, DefaultFont())
	return hwnd
}

var (
	fontOnce sync.Once
	font     uintptr
)

// DefaultFont returns Segoe UI 9pt.
func DefaultFont() uintptr {
	fontOnce.Do(func() {
		font = call(pCreateFontW, ^uintptr(15), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(W("Segoe UI"))))
	})
	return font
}

// BoldFont returns a larger semibold font.
func BoldFont(px int) uintptr {
	return call(pCreateFontW, ^uintptr(px-1), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(W("Segoe UI"))))
}

func SetFont(h windows.HWND, f uintptr) { SendMessage(h, WM_SETFONT, f, 1) }

func DefWindowProc(h windows.HWND, msg uint32, wp, lp uintptr) uintptr {
	return call(pDefWindowProcW, uintptr(h), uintptr(msg), wp, lp)
}
func DestroyWindow(h windows.HWND)       { call(pDestroyWindow, uintptr(h)) }
func ShowWindow(h windows.HWND, cmd int) { call(pShowWindow, uintptr(h), uintptr(cmd)) }
func UpdateWindow(h windows.HWND)        { call(pUpdateWindow, uintptr(h)) }
func PostMessage(h windows.HWND, m uint32, wp, lp uintptr) {
	call(pPostMessageW, uintptr(h), uintptr(m), wp, lp)
}
func SendMessage(h windows.HWND, m uint32, wp, lp uintptr) uintptr {
	return call(pSendMessageW, uintptr(h), uintptr(m), wp, lp)
}
func PostQuitMessage(code int) { call(pPostQuitMessage, uintptr(code)) }
func SetWindowPos(h windows.HWND, after uintptr, x, y, w, hh int32, flags uint32) {
	call(pSetWindowPos, uintptr(h), after, uintptr(x), uintptr(y), uintptr(w), uintptr(hh), uintptr(flags))
}
func MoveWindow(h windows.HWND, x, y, w, hh int32) {
	call(pMoveWindow, uintptr(h), uintptr(x), uintptr(y), uintptr(w), uintptr(hh), 1)
}
func ClientRect(h windows.HWND) RECT {
	var r RECT
	call(pGetClientRect, uintptr(h), uintptr(unsafe.Pointer(&r)))
	return r
}
func WindowRect(h windows.HWND) RECT {
	var r RECT
	call(pGetWindowRect, uintptr(h), uintptr(unsafe.Pointer(&r)))
	return r
}
func Invalidate(h windows.HWND) { call(pInvalidateRect, uintptr(h), 0, 0) }
func SetText(h windows.HWND, s string) {
	call(pSetWindowTextW, uintptr(h), uintptr(unsafe.Pointer(W(s))))
}
func GetText(h windows.HWND) string {
	n := call(pGetWindowTextLengthW, uintptr(h))
	buf := make([]uint16, n+1)
	call(pGetWindowTextW, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}
func Enable(h windows.HWND, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	call(pEnableWindow, uintptr(h), v)
}
func SetFocus(h windows.HWND)      { call(pSetFocus, uintptr(h)) }
func GetFocus() windows.HWND       { return windows.HWND(call(pGetFocus)) }
func SetForeground(h windows.HWND) { call(pSetForegroundWindow, uintptr(h)) }
func Foreground() windows.HWND     { return windows.HWND(call(pGetForegroundWindow)) }
func IsWindow(h windows.HWND) bool { return call(pIsWindow, uintptr(h)) != 0 }
func Checked(h windows.HWND) bool  { return SendMessage(h, BM_GETCHECK, 0, 0) == 1 }
func SetChecked(h windows.HWND, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	SendMessage(h, BM_SETCHECK, v, 0)
}
func SetTimer(h windows.HWND, id, ms uintptr) { call(pSetTimer, uintptr(h), id, ms, 0) }
func KillTimer(h windows.HWND, id uintptr)    { call(pKillTimer, uintptr(h), id) }
func SetCapture(h windows.HWND)               { call(pSetCapture, uintptr(h)) }
func ReleaseCapture()                         { call(pReleaseCapture) }
func KeyDown(vk int) bool                     { return call(pGetKeyState, uintptr(vk))&0x8000 != 0 }

var gwlStyle = ^uintptr(15) // GWL_STYLE = -16

func SetStyle(h windows.HWND, style uintptr) { call(pSetWindowLongPtrW, uintptr(h), gwlStyle, style) }
func Style(h windows.HWND) uintptr           { return call(pGetWindowLongPtrW, uintptr(h), gwlStyle) }

// MessageBox shows a standard message box.
func MessageBox(owner windows.HWND, text, title string, flags uint32) int {
	return int(call(pMessageBoxW, uintptr(owner), uintptr(unsafe.Pointer(W(text))), uintptr(unsafe.Pointer(W(title))), uintptr(flags)))
}

// SystemMetric wraps GetSystemMetrics.
func SystemMetric(i int) int32 { return int32(call(pGetSystemMetrics, uintptr(i))) }

// ------------------------------------------------------------ menus

func CreateMenu() uintptr      { return call(pCreateMenu) }
func CreatePopupMenu() uintptr { return call(pCreatePopupMenu) }
func AppendMenu(m uintptr, flags uint32, id uintptr, text string) {
	var p uintptr
	if text != "" {
		p = uintptr(unsafe.Pointer(W(text)))
	}
	call(pAppendMenuW, m, uintptr(flags), id, p)
}
func SetMenu(h windows.HWND, m uintptr) { call(pSetMenu, uintptr(h), m) }
func CheckMenu(m uintptr, id uintptr, on bool) {
	f := uintptr(MF_BYCOMMAND | MF_UNCHECKED)
	if on {
		f = MF_BYCOMMAND | MF_CHECKED
	}
	call(pCheckMenuItem, m, id, f)
}
func EnableMenu(m uintptr, id uintptr, on bool) {
	f := uintptr(MF_BYCOMMAND | MF_GRAYED)
	if on {
		f = MF_BYCOMMAND | MF_ENABLED
	}
	call(pEnableMenuItem, m, id, f)
}
func ClearMenu(m uintptr) {
	for call(pGetMenuItemCount, m) > 0 {
		call(pDeleteMenu, m, 0, MF_BYPOSITION)
	}
}
func DrawMenuBar(h windows.HWND) { call(pDrawMenuBar, uintptr(h)) }
func TrackPopup(m uintptr, h windows.HWND, x, y int32) uintptr {
	return call(pTrackPopupMenu, m, TPM_RETURNCMD, uintptr(x), uintptr(y), 0, uintptr(h), 0)
}
func CursorPos() POINT {
	var p POINT
	call(pGetCursorPos, uintptr(unsafe.Pointer(&p)))
	return p
}

// ------------------------------------------------------------ painting

func BeginPaint(h windows.HWND, ps *PAINTSTRUCT) windows.Handle {
	return windows.Handle(call(pBeginPaint, uintptr(h), uintptr(unsafe.Pointer(ps))))
}
func EndPaint(h windows.HWND, ps *PAINTSTRUCT) {
	call(pEndPaint, uintptr(h), uintptr(unsafe.Pointer(ps)))
}
func FillRect(dc windows.Handle, r *RECT, brush uintptr) {
	call(pFillRect, uintptr(dc), uintptr(unsafe.Pointer(r)), brush)
}
func SolidBrush(rgb uint32) uintptr { return call(pCreateSolidBrush, uintptr(rgb)) }
func StockObject(i int) uintptr     { return call(pGetStockObject, uintptr(i)) }

type BITMAPINFOHEADER struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// DrawBGRA stretches a top-down 32-bit BGRA buffer onto dc.
func DrawBGRA(dc windows.Handle, dx, dy, dw, dh int32, pix []byte, w, h int32) {
	if len(pix) == 0 {
		return
	}
	bi := BITMAPINFOHEADER{Width: w, Height: -h, Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	const halftone = 4
	call(pSetStretchBltMode, uintptr(dc), halftone)
	call(pStretchDIBits, uintptr(dc), uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh), 0, 0, uintptr(w), uintptr(h),
		uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0, 0x00CC0020)
}

// ------------------------------------------------------------ scroll bars

func SetScroll(h windows.HWND, bar int, max, page, pos int32) {
	si := SCROLLINFO{Mask: SIF_RANGE | SIF_PAGE | SIF_POS, Min: 0, Max: max, Page: uint32(page), Pos: pos}
	si.Size = uint32(unsafe.Sizeof(si))
	call(pSetScrollInfo, uintptr(h), uintptr(bar), uintptr(unsafe.Pointer(&si)), 1)
}
func GetScroll(h windows.HWND, bar int) SCROLLINFO {
	si := SCROLLINFO{Mask: SIF_ALL}
	si.Size = uint32(unsafe.Sizeof(si))
	call(pGetScrollInfo, uintptr(h), uintptr(bar), uintptr(unsafe.Pointer(&si)))
	return si
}

// ------------------------------------------------------------ message loop

// UI runs a Win32 message loop on a locked OS thread and lets other
// goroutines run code on it.
type UI struct {
	hwnd windows.HWND
	mu   sync.Mutex
	q    []func()
	tid  uint32
}

const wmRun = WM_APP + 1

// NewUI starts the UI thread, runs setup on it and returns once setup is
// done. Dialogs passed to IsDialogMessage get Tab navigation.
func NewUI(setup func(ui *UI)) *UI {
	ui := &UI{}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		RegisterClass("RSMDispatcher", 0)
		ui.hwnd = CreateWindow(0, "RSMDispatcher", "", 0, 0, 0, 0, 0, windows.HWND(^uintptr(2)) /* HWND_MESSAGE */, 0, func(h windows.HWND, m uint32, wp, lp uintptr) (uintptr, bool) {
			if m == wmRun {
				ui.drain()
				return 0, true
			}
			return 0, false
		})
		setup(ui)
		close(ready)
		var msg MSG
		for {
			r := call(pGetMessageW, uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if r == 0 || r == ^uintptr(0) {
				return
			}
			if DialogRoot(msg.Hwnd) != 0 && call(pIsDialogMessageW, uintptr(DialogRoot(msg.Hwnd)), uintptr(unsafe.Pointer(&msg))) != 0 {
				continue
			}
			call(pTranslateMessage, uintptr(unsafe.Pointer(&msg)))
			call(pDispatchMessageW, uintptr(unsafe.Pointer(&msg)))
		}
	}()
	<-ready
	return ui
}

var (
	dlgMu       sync.Mutex
	dlgRoots    = map[windows.HWND]bool{}
	getAncestor = user32.NewProc("GetAncestor")
)

// RegisterDialog enables Tab/Enter keyboard navigation in a window.
func RegisterDialog(h windows.HWND) { dlgMu.Lock(); dlgRoots[h] = true; dlgMu.Unlock() }

// UnregisterDialog removes it again.
func UnregisterDialog(h windows.HWND) { dlgMu.Lock(); delete(dlgRoots, h); dlgMu.Unlock() }

// DialogRoot returns the registered dialog containing h, or 0.
func DialogRoot(h windows.HWND) windows.HWND {
	if h == 0 {
		return 0
	}
	root := windows.HWND(call(getAncestor, uintptr(h), 2)) // GA_ROOT
	dlgMu.Lock()
	defer dlgMu.Unlock()
	if dlgRoots[root] {
		return root
	}
	return 0
}

func (ui *UI) drain() {
	ui.mu.Lock()
	q := ui.q
	ui.q = nil
	ui.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// Post runs f on the UI thread asynchronously.
func (ui *UI) Post(f func()) {
	ui.mu.Lock()
	ui.q = append(ui.q, f)
	ui.mu.Unlock()
	PostMessage(ui.hwnd, wmRun, 0, 0)
}

// Do runs f on the UI thread and waits for it.
func (ui *UI) Do(f func()) {
	done := make(chan struct{})
	ui.Post(func() { f(); close(done) })
	<-done
}

// Quit ends the message loop.
func (ui *UI) Quit() { ui.Post(func() { PostQuitMessage(0) }) }

// WM_CTLCOLORSTATIC lets a parent colour its STATIC children.
const WM_CTLCOLORSTATIC = 0x0138

// WS_CLIPSIBLINGS keeps a child from painting over overlapping siblings.
const WS_CLIPSIBLINGS = 0x04000000

var (
	pSetBkMode    = gdi32.NewProc("SetBkMode")
	pSetTextColor = gdi32.NewProc("SetTextColor")
)

// TransparentText makes a control's text background transparent and sets
// its colour (0x00BBGGRR).
func TransparentText(dc uintptr, rgb uint32) {
	call(pSetBkMode, dc, 1) // TRANSPARENT
	call(pSetTextColor, dc, uintptr(rgb))
}
