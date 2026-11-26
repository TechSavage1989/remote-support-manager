//go:build windows

package winapi

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	lvmFirst            = 0x1000
	lvmDeleteAllItems   = lvmFirst + 9
	lvmGetNextItem      = lvmFirst + 12
	lvmInsertColumnW    = lvmFirst + 97
	lvmInsertItemW      = lvmFirst + 77
	lvmSetItemW         = lvmFirst + 76
	lvmGetItemCount     = lvmFirst + 4
	lvmSetExtendedStyle = lvmFirst + 54
	lvmEnsureVisible    = lvmFirst + 19

	LVS_REPORT           = 0x1
	LVS_SHOWSELALWAYS    = 0x8
	LVS_SINGLESEL        = 0x4
	LVS_EX_FULLROWSELECT = 0x20
	LVS_EX_DOUBLEBUFFER  = 0x10000

	lvifText     = 0x1
	lvifParam    = 0x4
	lvniSelected = 0x2

	NM_DBLCLK     = ^uintptr(2)   // -3
	LVN_BEGINDRAG = ^uintptr(108) // -109
	LVN_KEYDOWN   = ^uintptr(154) // -155
)

type lvColumn struct {
	Mask    uint32
	Fmt     int32
	Cx      int32
	Text    *uint16
	TextMax int32
	SubItem int32
	Image   int32
	Order   int32
	CxMin   int32
	CxDef   int32
	CxIdeal int32
}

type lvItem struct {
	Mask      uint32
	Item      int32
	SubItem   int32
	State     uint32
	StateMask uint32
	Text      *uint16
	TextMax   int32
	Image     int32
	Param     uintptr
	Indent    int32
	GroupID   int32
	Columns   uint32
	ColPtr    uintptr
	ColFmt    uintptr
	Group     int32
}

// NMHDR is the WM_NOTIFY header.
type NMHDR struct {
	From windows.HWND
	ID   uintptr
	Code uintptr
}

// ListView wraps a report-style list view.
type ListView struct {
	H windows.HWND
}

// NewListView creates a list view with the given columns (name, width).
func NewListView(parent windows.HWND, id int, x, y, w, h int32, multi bool, cols ...any) *ListView {
	style := uint32(LVS_REPORT | LVS_SHOWSELALWAYS | WS_TABSTOP)
	if !multi {
		style |= LVS_SINGLESEL
	}
	hwnd := ControlEx(parent, WS_EX_CLIENTEDGE, "SysListView32", "", style, x, y, w, h, id)
	SendMessage(hwnd, lvmSetExtendedStyle, 0, LVS_EX_FULLROWSELECT|LVS_EX_DOUBLEBUFFER)
	lv := &ListView{H: hwnd}
	for i := 0; i+1 < len(cols); i += 2 {
		c := lvColumn{Mask: 0x1 | 0x2 | 0x4, Cx: int32(cols[i+1].(int)), Text: W(cols[i].(string))}
		SendMessage(hwnd, lvmInsertColumnW, uintptr(i/2), uintptr(unsafe.Pointer(&c)))
	}
	return lv
}

// Clear removes all rows.
func (l *ListView) Clear() { SendMessage(l.H, lvmDeleteAllItems, 0, 0) }

// Count returns the number of rows.
func (l *ListView) Count() int { return int(SendMessage(l.H, lvmGetItemCount, 0, 0)) }

// Add appends a row and returns its index.
func (l *ListView) Add(cells ...string) int {
	it := lvItem{Mask: lvifText, Item: int32(l.Count()), Text: W(cells[0])}
	idx := int(SendMessage(l.H, lvmInsertItemW, 0, uintptr(unsafe.Pointer(&it))))
	for i := 1; i < len(cells); i++ {
		l.Set(idx, i, cells[i])
	}
	return idx
}

// Set changes one cell.
func (l *ListView) Set(row, col int, text string) {
	it := lvItem{Mask: lvifText, Item: int32(row), SubItem: int32(col), Text: W(text)}
	SendMessage(l.H, lvmSetItemW, 0, uintptr(unsafe.Pointer(&it)))
}

// Selected returns selected row indexes.
func (l *ListView) Selected() []int {
	out := []int{}
	i := -1
	for {
		n := int(int32(SendMessage(l.H, lvmGetNextItem, uintptr(i), lvniSelected)))
		if n < 0 {
			return out
		}
		out = append(out, n)
		i = n
	}
}

// EnsureVisible scrolls to a row.
func (l *ListView) EnsureVisible(row int) { SendMessage(l.H, lvmEnsureVisible, uintptr(row), 0) }
