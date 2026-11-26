//go:build windows

package updateui

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/TechSavage1989/remote-support-manager/internal/winapi"
)

// Theme holds the colours the update UI is drawn with (0x00BBGGRR).
type Theme struct {
	Bg, Card, Surface, Line, Text, Muted, Accent, SoftAccent, Green, SoftGreen, Red, SoftRed uint32
}

// CurrentTheme follows the Windows app light/dark setting.
func CurrentTheme() Theme {
	if winapi.DarkMode() {
		return Theme{
			Bg: winapi.RGB(17, 24, 39), Card: winapi.RGB(30, 41, 59), Surface: winapi.RGB(15, 23, 42), Line: winapi.RGB(51, 65, 85),
			Text: winapi.RGB(241, 245, 249), Muted: winapi.RGB(148, 163, 184), Accent: winapi.RGB(96, 165, 250), SoftAccent: winapi.RGB(30, 58, 138),
			Green: winapi.RGB(74, 222, 128), SoftGreen: winapi.RGB(20, 83, 45), Red: winapi.RGB(248, 113, 113), SoftRed: winapi.RGB(127, 29, 29),
		}
	}
	return Theme{
		Bg: winapi.RGB(241, 245, 249), Card: winapi.RGB(255, 255, 255), Surface: winapi.RGB(248, 250, 252), Line: winapi.RGB(226, 232, 240),
		Text: winapi.RGB(15, 23, 42), Muted: winapi.RGB(71, 85, 105), Accent: winapi.RGB(29, 78, 216), SoftAccent: winapi.RGB(219, 234, 254),
		Green: winapi.RGB(21, 128, 61), SoftGreen: winapi.RGB(220, 252, 231), Red: winapi.RGB(185, 28, 28), SoftRed: winapi.RGB(254, 226, 226),
	}
}

func (t Theme) stateColor(s State) (strong, soft uint32) {
	switch s.Phase {
	case Ready, UpToDate:
		return t.Green, t.SoftGreen
	case Failed:
		return t.Red, t.SoftRed
	}
	return t.Accent, t.SoftAccent
}

const (
	pad     = 16
	btnH    = 30
	cardRad = 12
	noteMax = 4
	cardW   = 380
)

// RenderUpdateCard draws the card body for s inside r (or only measures it
// when dc is 0) and returns the y where the button row starts and the total
// height. The buttons themselves are real controls placed by the caller.
func RenderUpdateCard(dc windows.Handle, r winapi.RECT, s State, t Theme, allNotes bool) (buttonsY, height int32) {
	draw := dc != 0
	w := r.Right - r.Left
	strong, soft := t.stateColor(s)
	if draw {
		winapi.Rounded(dc, r, cardRad, t.Card, t.Line)
		winapi.Fill(dc, winapi.RECT{Left: r.Left + 6, Top: r.Top + 1, Right: r.Right - 6, Bottom: r.Top + 4}, strong) // 3 px top accent
	}
	x, y := r.Left+pad, r.Top+pad
	// Icon tile.
	if draw {
		winapi.Rounded(dc, winapi.RECT{Left: x, Top: y, Right: x + 40, Bottom: y + 40}, 10, soft, soft)
		glyph := map[Phase]string{Ready: "✓", UpToDate: "✓", Failed: "!", Checking: "↻", Installing: "↻"}[s.Phase]
		if glyph == "" {
			glyph = "↓"
		}
		winapi.Text(dc, glyph, winapi.RECT{Left: x, Top: y, Right: x + 40, Bottom: y + 40}, winapi.Font(20, 600), strong, winapi.DT_CENTER|winapi.DT_VCENTER|winapi.DT_SINGLELINE)
		winapi.Text(dc, s.AppName, winapi.RECT{Left: x + 52, Top: y, Right: r.Right - pad, Bottom: y + 16}, winapi.Font(12, 400), t.Muted, winapi.DT_SINGLELINE|winapi.DT_END_ELLIPSIS)
		winapi.Text(dc, s.Title(), winapi.RECT{Left: x + 52, Top: y + 16, Right: r.Right - pad, Bottom: y + 40}, winapi.Font(16, 600), t.Text, winapi.DT_SINGLELINE|winapi.DT_END_ELLIPSIS)
	}
	y += 48
	sentence := s.Sentence()
	mdc := dc
	var tmp windows.Handle
	if !draw {
		tmp = winapi.WindowDC(0)
		mdc = tmp
		defer winapi.ReleaseWindowDC(0, tmp)
	}
	sh := winapi.MeasureText(mdc, sentence, w-2*pad, winapi.Font(13, 400), winapi.DT_WORDBREAK)
	if draw {
		winapi.Text(dc, sentence, winapi.RECT{Left: x, Top: y, Right: r.Right - pad, Bottom: y + sh}, winapi.Font(13, 400), t.Muted, winapi.DT_WORDBREAK)
	}
	y += sh + 10
	// Version tags.
	y += renderTags(dc, mdc, x, y, s, t)
	// What's new.
	if s.M != nil && strings.TrimSpace(s.M.Notes) != "" && s.Phase != UpToDate {
		h := RenderWhatsNew(dc, mdc, winapi.RECT{Left: x, Top: y, Right: r.Right - pad}, s, t, allNotes)
		y += h + 10
	}
	// Download bar.
	if s.Phase == Downloading || s.Phase == Verifying {
		y += RenderDownloadBar(dc, mdc, winapi.RECT{Left: x, Top: y, Right: r.Right - pad}, s, t) + 10
	}
	buttonsY = y
	return buttonsY, y + btnH + pad - r.Top
}

func renderTags(dc, mdc windows.Handle, x, y int32, s State, t Theme) int32 {
	f := winapi.Font(12, 600)
	tag := func(text string, bg, fg, line uint32) {
		tw := winapi.TextWidth(mdc, text, f) + 14
		if dc != 0 {
			winapi.Rounded(dc, winapi.RECT{Left: x, Top: y, Right: x + tw, Bottom: y + 22}, 8, bg, line)
			winapi.Text(dc, text, winapi.RECT{Left: x, Top: y, Right: x + tw, Bottom: y + 22}, f, fg, winapi.DT_CENTER|winapi.DT_VCENTER|winapi.DT_SINGLELINE)
		}
		x += tw + 6
	}
	arrow := func() {
		if dc != 0 {
			winapi.Text(dc, "→", winapi.RECT{Left: x, Top: y, Right: x + 18, Bottom: y + 22}, f, t.Muted, winapi.DT_CENTER|winapi.DT_VCENTER|winapi.DT_SINGLELINE)
		}
		x += 24
	}
	if s.M == nil || s.M.Version == s.Installed {
		tag("Installed "+s.Installed, t.Card, t.Text, t.Line)
		return 30
	}
	tag(s.Installed, t.Card, t.Text, t.Line)
	arrow()
	tag(s.M.Version, t.SoftAccent, t.Accent, t.Line)
	if s.M.Size > 0 {
		tag(humanSize(s.M.Size), t.Card, t.Muted, t.Line)
	}
	return 30
}

// RenderWhatsNew draws the "What's new" card (up to four highlights, or all
// notes when all is true) and returns its height. Text is drawn with GDI,
// which never interprets markup.
func RenderWhatsNew(dc, mdc windows.Handle, r winapi.RECT, s State, t Theme, all bool) int32 {
	lines := ReleaseNoteLines(s.M.Notes)
	shown, more := lines, false
	if !all {
		shown, more = Highlights(lines, noteMax)
	}
	inner := r.Right - r.Left - 28
	body := winapi.Font(13, 400)
	h := int32(34)
	heights := make([]int32, len(shown))
	for i, l := range shown {
		text := l.Text
		if l.Kind == NoteBullet {
			text = "•  " + text
		}
		f := body
		if l.Kind == NoteHeading {
			f = winapi.Font(11, 600)
		}
		heights[i] = winapi.MeasureText(mdc, text, inner, f, winapi.DT_WORDBREAK) + 4
		h += heights[i]
	}
	if more {
		h += 20
	}
	h += 8
	if dc == 0 {
		return h
	}
	box := winapi.RECT{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Top + h}
	winapi.Rounded(dc, box, 8, t.Surface, t.Line)
	winapi.Fill(dc, winapi.RECT{Left: r.Left + 1, Top: r.Top + 4, Right: r.Left + 4, Bottom: box.Bottom - 4}, t.Accent)
	winapi.Text(dc, "What's new", winapi.RECT{Left: r.Left + 14, Top: r.Top + 8, Right: r.Right - 10, Bottom: r.Top + 28}, winapi.Font(13, 600), t.Text, winapi.DT_SINGLELINE)
	if s.M != nil {
		winapi.Text(dc, s.Installed+" → "+s.M.Version, winapi.RECT{Left: r.Left + 14, Top: r.Top + 8, Right: r.Right - 12, Bottom: r.Top + 28}, winapi.Font(12, 400), t.Muted, winapi.DT_SINGLELINE|winapi.DT_RIGHT)
	}
	y := r.Top + 34
	for i, l := range shown {
		text, f, c := l.Text, body, t.Text
		if l.Kind == NoteBullet {
			text = "•  " + text
		}
		if l.Kind == NoteHeading {
			f, c = winapi.Font(11, 600), t.Muted
		}
		winapi.Text(dc, text, winapi.RECT{Left: r.Left + 14, Top: y, Right: r.Left + 14 + inner, Bottom: y + heights[i]}, f, c, winapi.DT_WORDBREAK)
		y += heights[i]
	}
	if more {
		winapi.Text(dc, "All release notes are shown when you review the update.", winapi.RECT{Left: r.Left + 14, Top: y, Right: r.Right - 10, Bottom: y + 18}, winapi.Font(12, 400), t.Muted, winapi.DT_SINGLELINE|winapi.DT_END_ELLIPSIS)
	}
	return h
}

// RenderDownloadBar draws the label row, the 9 px bar and the verification
// note, and returns the height used.
func RenderDownloadBar(dc, mdc windows.Handle, r winapi.RECT, s State, t Theme) int32 {
	const h = 18 + 9 + 6 + 18
	if dc == 0 {
		return h
	}
	pct := int64(0)
	if s.Total > 0 {
		pct = s.Done * 100 / s.Total
	}
	left := fmt.Sprintf("%s of %s", humanSize(s.Done), humanSize(s.Total))
	if s.Phase == Verifying {
		left = "Verifying download"
	}
	winapi.Text(dc, left, winapi.RECT{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Top + 18}, winapi.Font(12, 400), t.Muted, winapi.DT_SINGLELINE)
	winapi.Text(dc, fmt.Sprintf("%d%%", pct), winapi.RECT{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Top + 18}, winapi.Font(12, 600), t.Text, winapi.DT_SINGLELINE|winapi.DT_RIGHT)
	track := winapi.RECT{Left: r.Left, Top: r.Top + 20, Right: r.Right, Bottom: r.Top + 29}
	winapi.Rounded(dc, track, 9, t.Line, t.Line)
	fill := track
	fill.Right = track.Left + int32(int64(track.Right-track.Left)*pct/100)
	color := t.Accent
	if s.Phase == Verifying || pct >= 100 {
		color = t.Green
	}
	if fill.Right-fill.Left >= 9 {
		winapi.Rounded(dc, fill, 9, color, color)
	}
	winapi.Text(dc, "✓ Checked against the release's SHA-256 digest before it can be installed.", winapi.RECT{Left: r.Left, Top: r.Top + 33, Right: r.Right, Bottom: r.Top + h}, winapi.Font(12, 400), t.Green, winapi.DT_SINGLELINE|winapi.DT_END_ELLIPSIS)
	return h
}

// ------------------------------------------------------------ card window

// Card is the update card as a child window: the notice in the app's
// bottom-right corner, or the card inside the settings section.
type Card struct {
	H        windows.HWND
	f        *Flow
	st       State
	theme    Theme
	settings bool
	primary  windows.HWND
	later    windows.HWND
	details  windows.HWND
	height   int32
	place    func()
	// OnDetails opens the settings section (notice only).
	OnDetails func()
}

const (
	idPrimary = 501
	idLater   = 502
	idDetails = 503
)

// NewCard creates a card inside parent. A notice card (settings=false)
// shows itself only when there is something to act on.
func NewCard(parent windows.HWND, f *Flow, settings bool) *Card {
	c := &Card{f: f, settings: settings, theme: CurrentTheme()}
	winapi.RegisterClass("RSMUpdateCard", 0)
	style := uint32(winapi.WS_CHILD | winapi.WS_CLIPCHILDREN)
	if settings {
		style |= winapi.WS_VISIBLE
	}
	c.H = winapi.CreateWindow(0, "RSMUpdateCard", "App updates", style, 0, 0, cardW, 200, parent, 0, c.proc)
	c.primary = winapi.Control(c.H, "BUTTON", "Download update", winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, 0, 0, 150, btnH, idPrimary)
	c.later = winapi.Control(c.H, "BUTTON", "Later", winapi.WS_TABSTOP, 0, 0, 80, btnH, idLater)
	if !settings {
		c.details = winapi.Control(c.H, "BUTTON", "Details", winapi.WS_TABSTOP, 0, 0, 80, btnH, idDetails)
	}
	return c
}

// Visible reports whether the notice should be on screen for s.
func noticeVisible(s State) bool {
	if s.Postponed || s.Phase == Installing {
		return false
	}
	switch s.Phase {
	case Available, Downloading, Verifying, Ready:
		return true
	case Failed:
		return s.M != nil // a failed download; automatic check failures stay in settings
	}
	return false
}

// Update re-renders the card for s without moving keyboard focus.
func (c *Card) Update(s State) {
	c.st = s
	c.theme = CurrentTheme()
	focus := winapi.GetFocus()
	label, enabled := "Check for updates", true
	switch s.Phase {
	case Available:
		label = "Download update"
	case Downloading, Verifying:
		label, enabled = "Downloading...", false
	case Ready:
		label = "Review and restart"
	case Failed:
		if s.M != nil {
			label = "Retry download"
		} else {
			label = "Check again"
		}
	case Checking:
		label, enabled = "Checking...", false
	case Installing:
		label, enabled = "Installing...", false
	}
	if winapi.GetText(c.primary) != label {
		winapi.SetText(c.primary, label)
	}
	winapi.Enable(c.primary, enabled)
	showLater := s.Phase == Available || s.Phase == Ready
	winapi.ShowWindow(c.later, map[bool]int{true: winapi.SW_SHOW, false: winapi.SW_HIDE}[showLater])
	_, h := RenderUpdateCard(0, winapi.RECT{Right: cardW}, s, c.theme, false)
	c.height = h
	if c.settings {
		winapi.ShowWindow(c.H, winapi.SW_SHOW)
	} else {
		winapi.ShowWindow(c.H, map[bool]int{true: winapi.SW_SHOW, false: winapi.SW_HIDE}[noticeVisible(s)])
	}
	if c.place != nil {
		c.place()
	}
	c.layoutButtons()
	winapi.Invalidate(c.H)
	if focus != 0 && winapi.IsWindow(focus) {
		winapi.SetFocus(focus)
	}
}

// PlaceBottomRight keeps a notice card in the bottom-right corner of its
// parent (full width minus margins on narrow windows).
func (c *Card) PlaceBottomRight(parent windows.HWND, bottomInset int32) {
	c.place = func() {
		pr := winapi.ClientRect(parent)
		w := int32(cardW)
		if pr.Right < w+32 {
			w = pr.Right - 32
		}
		winapi.SetWindowPos(c.H, 0 /* HWND_TOP */, pr.Right-w-16, pr.Bottom-c.height-16-bottomInset, w, c.height, 0)
	}
	c.place()
}

// Height is the card's current content height.
func (c *Card) Height() int32 { return c.height }

func (c *Card) layoutButtons() {
	r := winapi.ClientRect(c.H)
	by, _ := RenderUpdateCard(0, winapi.RECT{Right: r.Right}, c.st, c.theme, false)
	x := int32(pad)
	winapi.MoveWindow(c.primary, x, by, 160, btnH)
	x += 168
	if c.details != 0 {
		winapi.MoveWindow(c.details, x, by, 80, btnH)
		x += 88
	}
	winapi.MoveWindow(c.later, x, by, 80, btnH)
}

func (c *Card) proc(h windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case winapi.WM_ERASEBKGND:
		return 1, true
	case winapi.WM_SIZE:
		c.layoutButtons()
		winapi.Invalidate(h)
		return 0, false
	case winapi.WM_PAINT:
		var ps winapi.PAINTSTRUCT
		dc := winapi.BeginPaint(h, &ps)
		r := winapi.ClientRect(h)
		bg := c.theme.Bg
		if c.settings {
			bg = winapi.SysColor(winapi.COLOR_BTNFACE)
		}
		winapi.Fill(dc, r, bg)
		RenderUpdateCard(dc, winapi.RECT{Left: 0, Top: 0, Right: r.Right - 1, Bottom: r.Bottom - 1}, c.st, c.theme, false)
		winapi.EndPaint(h, &ps)
		return 0, true
	case winapi.WM_COMMAND:
		switch wp & 0xffff {
		case idPrimary:
			switch c.st.Phase {
			case Available:
				c.f.Download()
			case Ready:
				ShowReviewDialog(winapi.DialogRoot(h), c.f)
			case Failed:
				if c.st.M != nil {
					c.f.Download()
				} else {
					c.f.Check(true)
				}
			default:
				c.f.Check(true)
			}
		case idLater:
			c.f.Later()
		case idDetails:
			if c.OnDetails != nil {
				c.OnDetails()
			}
		}
		return 0, true
	}
	return 0, false
}

// ------------------------------------------------------------ review dialog

// ShowReviewDialog asks before installing and restarting. Install errors are
// shown inline and the dialog stays open so the user can retry.
func ShowReviewDialog(owner windows.HWND, f *Flow) {
	s := f.State()
	if s.M == nil {
		return
	}
	t := CurrentTheme()
	winapi.RegisterClass("RSMUpdateReview", 0)
	const w = 560
	var h windows.HWND
	var errText string
	var install windows.HWND
	notesH := int32(0)
	{
		dc := winapi.WindowDC(0)
		notesH = RenderWhatsNew(0, dc, winapi.RECT{Right: w - 2*pad - 16}, s, t, true)
		winapi.ReleaseWindowDC(0, dc)
	}
	assurances := []string{
		"The app closes, installs the verified update and reopens.",
		"An active support session is never interrupted: install waits until it ends.",
		"Your settings are kept, and the previous version is kept for recovery.",
	}
	bodyTop := int32(64)
	assureTop := bodyTop + notesH + 12
	mutedTop := assureTop + int32(len(assurances))*26 + 6
	errTop := mutedTop + 24
	footer := errTop + 36
	total := footer + btnH + pad + 40
	if owner == 0 {
		owner = f.Owner()
	}
	or := winapi.WindowRect(owner)
	x := or.Left + (or.Right-or.Left-w)/2
	y := or.Top + 60
	h = winapi.CreateWindow(winapi.WS_EX_DLGMODALFRAME, "RSMUpdateReview", "Review update", winapi.WS_CAPTION|winapi.WS_SYSMENU|winapi.WS_VISIBLE,
		x, y, w, total, owner, 0, func(hh windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
			switch msg {
			case winapi.WM_ERASEBKGND:
				return 1, true
			case winapi.WM_PAINT:
				var ps winapi.PAINTSTRUCT
				dc := winapi.BeginPaint(hh, &ps)
				r := winapi.ClientRect(hh)
				winapi.Fill(dc, r, t.Card)
				winapi.Text(dc, fmt.Sprintf("Install %s %s?", s.AppName, s.M.Version), winapi.RECT{Left: pad, Top: pad, Right: r.Right - pad, Bottom: pad + 26}, winapi.Font(18, 600), t.Text, winapi.DT_SINGLELINE|winapi.DT_END_ELLIPSIS)
				winapi.Text(dc, "Review the update before restarting", winapi.RECT{Left: pad, Top: pad + 26, Right: r.Right - pad, Bottom: pad + 44}, winapi.Font(13, 400), t.Muted, winapi.DT_SINGLELINE)
				RenderWhatsNew(dc, dc, winapi.RECT{Left: pad, Top: bodyTop, Right: r.Right - pad}, s, t, true)
				yy := assureTop
				for _, a := range assurances {
					winapi.Rounded(dc, winapi.RECT{Left: pad, Top: yy + 2, Right: pad + 18, Bottom: yy + 20}, 18, t.SoftGreen, t.SoftGreen)
					winapi.Text(dc, "✓", winapi.RECT{Left: pad, Top: yy + 2, Right: pad + 18, Bottom: yy + 20}, winapi.Font(12, 600), t.Green, winapi.DT_CENTER|winapi.DT_VCENTER|winapi.DT_SINGLELINE)
					winapi.Text(dc, a, winapi.RECT{Left: pad + 28, Top: yy, Right: r.Right - pad, Bottom: yy + 22}, winapi.Font(13, 400), t.Text, winapi.DT_SINGLELINE|winapi.DT_VCENTER|winapi.DT_END_ELLIPSIS)
					yy += 26
				}
				winapi.Text(dc, "Finish any file transfers and close other copies of the app first.", winapi.RECT{Left: pad, Top: mutedTop, Right: r.Right - pad, Bottom: mutedTop + 20}, winapi.Font(12, 400), t.Muted, winapi.DT_SINGLELINE)
				if errText != "" {
					winapi.Text(dc, errText, winapi.RECT{Left: pad, Top: errTop, Right: r.Right - pad, Bottom: errTop + 34}, winapi.Font(12, 600), t.Red, winapi.DT_WORDBREAK)
				}
				winapi.EndPaint(hh, &ps)
				return 0, true
			case winapi.WM_COMMAND:
				switch wp & 0xffff {
				case winapi.IDOK, 601:
					winapi.Enable(install, false)
					if err := f.Install(); err != nil {
						errText = "Not installed: " + err.Error()
						winapi.SetText(install, "Try again")
						winapi.Enable(install, true)
						winapi.Invalidate(hh)
						return 0, true
					}
					winapi.UnregisterDialog(hh)
					winapi.DestroyWindow(hh)
				case winapi.IDCANCEL:
					winapi.UnregisterDialog(hh)
					winapi.DestroyWindow(hh)
				}
				return 0, true
			case winapi.WM_CLOSE:
				winapi.UnregisterDialog(hh)
				winapi.DestroyWindow(hh)
				return 0, true
			}
			return 0, false
		})
	cr := winapi.ClientRect(h)
	winapi.Control(h, "BUTTON", "Cancel", winapi.WS_TABSTOP, cr.Right-pad-160-8-100, footer, 100, btnH, winapi.IDCANCEL)
	install = winapi.Control(h, "BUTTON", "Install and restart", winapi.BS_DEFPUSHBUTTON|winapi.WS_TABSTOP, cr.Right-pad-160, footer, 160, btnH, 601)
	winapi.RegisterDialog(h)
	winapi.SetFocus(install)
}

// ------------------------------------------------------------ settings

// ShowSettings opens the "App updates" section: the same card, a "Check for
// updates" button in the header, the automatic-check preference and an
// "Update information" expander.
func ShowSettings(owner windows.HWND, f *Flow, releasesURL string) {
	winapi.RegisterClass("RSMUpdateSettings", uintptr(winapi.COLOR_BTNFACE+1))
	var card *Card
	var auto, info, infoBtn, link windows.HWND
	expanded := false
	var unsub func()
	var h windows.HWND
	layout := func() {
		r := winapi.ClientRect(h)
		ch := card.Height()
		winapi.MoveWindow(card.H, pad, 52, r.Right-2*pad, ch)
		y := 52 + ch + 12
		winapi.MoveWindow(auto, pad, y, r.Right-2*pad, 22)
		winapi.MoveWindow(infoBtn, pad, y+30, 200, 26)
		winapi.ShowWindow(info, map[bool]int{true: winapi.SW_SHOW, false: winapi.SW_HIDE}[expanded])
		winapi.MoveWindow(info, pad, y+62, r.Right-2*pad, 96)
		ly := y + 62
		if expanded {
			ly += 104
		}
		winapi.MoveWindow(link, pad, ly, 130, 26)
	}
	render := func(s State) {
		card.Update(s)
		winapi.SetChecked(auto, s.AutoCheck)
		last, next := "never", "after the next start"
		if !s.LastCheck.IsZero() {
			last = s.LastCheck.Format("2006-01-02 15:04")
		}
		if !s.NextCheck.IsZero() && s.AutoCheck {
			next = s.NextCheck.Format("15:04")
		} else if !s.AutoCheck {
			next = "only when you check"
		}
		winapi.SetText(info, fmt.Sprintf("Last check: %s\r\nNext check: %s\r\nSource: %s\r\nVerification: the release information must carry the publisher's Ed25519 signature, and the download must match its size and SHA-256 digest and be a Windows program before it can be installed.",
			last, next, f.Checker.FeedURL))
		layout()
	}
	h = winapi.CreateWindow(0, "RSMUpdateSettings", "App updates · "+f.AppName, winapi.WS_CAPTION|winapi.WS_SYSMENU|winapi.WS_VISIBLE|winapi.WS_CLIPCHILDREN,
		-0x80000000, -0x80000000, cardW+2*pad+16, 560, owner, 0, func(hh windows.HWND, msg uint32, wp, lp uintptr) (uintptr, bool) {
			switch msg {
			case winapi.WM_COMMAND:
				switch wp & 0xffff {
				case 701:
					f.Check(true)
				case 702:
					f.SetAuto(winapi.Checked(auto))
				case 703:
					expanded = !expanded
					label := "▸ Update information"
					if expanded {
						label = "▾ Update information"
					}
					winapi.SetText(infoBtn, label)
					layout()
				case 704:
					winapi.OpenURL(releasesURL)
				}
				return 0, true
			case winapi.WM_CLOSE:
				if unsub != nil {
					unsub()
				}
				winapi.UnregisterDialog(hh)
				winapi.DestroyWindow(hh)
				return 0, true
			}
			return 0, false
		})
	title := winapi.Control(h, "STATIC", "App updates", winapi.SS_LEFT, pad, 14, 200, 26, 0)
	winapi.SetFont(title, winapi.Font(18, 600))
	winapi.Control(h, "BUTTON", "Check for updates", winapi.WS_TABSTOP, cardW+pad-150, 12, 150, 28, 701)
	card = NewCard(h, f, true)
	auto = winapi.Control(h, "BUTTON", "Check for updates automatically (checking never downloads)", winapi.BS_AUTOCHECKBOX|winapi.WS_TABSTOP, 0, 0, 10, 10, 702)
	infoBtn = winapi.Control(h, "BUTTON", "▸ Update information", winapi.WS_TABSTOP, 0, 0, 10, 10, 703)
	info = winapi.ControlEx(h, winapi.WS_EX_CLIENTEDGE, "EDIT", "", winapi.ES_MULTILINE|winapi.ES_READONLY|winapi.ES_AUTOVSCROLL, 0, 0, 10, 10, 0)
	link = winapi.Control(h, "BUTTON", "Release history", winapi.WS_TABSTOP, 0, 0, 10, 10, 704)
	unsub = f.Subscribe(render)
	render(f.State())
	winapi.RegisterDialog(h)
}
