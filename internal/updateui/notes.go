// Release-note parsing is platform-neutral so it can be tested anywhere.
// The Windows drawing code lives in the *_windows.go files.

package updateui

import "strings"

// NoteKind is the kind of one rendered release-note line.
type NoteKind int

const (
	NoteText NoteKind = iota
	NoteBullet
	NoteHeading // shown as a small uppercase label
)

// NoteLine is one line of safely rendered release notes.
type NoteLine struct {
	Kind NoteKind
	Text string
}

// ReleaseNoteLines turns Markdown release notes into plain lines. Only
// bullets, headings, bold and inline code are recognised, and their markers
// are removed; everything else, including HTML such as <img onerror=...>,
// is kept as literal text. The result is drawn with GDI text calls, which
// never interpret markup.
func ReleaseNoteLines(md string) []NoteLine {
	var out []NoteLine
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		kind := NoteText
		switch {
		case strings.HasPrefix(l, "#"):
			kind = NoteHeading
			l = strings.ToUpper(strings.TrimSpace(strings.TrimLeft(l, "#")))
		case strings.HasPrefix(l, "- "), strings.HasPrefix(l, "* "), strings.HasPrefix(l, "+ "):
			kind = NoteBullet
			l = strings.TrimSpace(l[2:])
		}
		l = strings.ReplaceAll(l, "**", "")
		l = strings.ReplaceAll(l, "`", "")
		l = strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' {
				return -1
			}
			return r
		}, l)
		if l != "" {
			out = append(out, NoteLine{Kind: kind, Text: l})
		}
	}
	return out
}

// Highlights returns up to n bullet lines (or text lines when there are no
// bullets) and whether more content exists.
func Highlights(lines []NoteLine, n int) ([]NoteLine, bool) {
	var b []NoteLine
	for _, l := range lines {
		if l.Kind == NoteBullet {
			b = append(b, l)
		}
	}
	if len(b) == 0 {
		for _, l := range lines {
			if l.Kind == NoteText {
				b = append(b, l)
			}
		}
	}
	more := len(b) > n || len(lines) > len(b)
	if len(b) > n {
		b = b[:n]
	}
	return b, more
}
