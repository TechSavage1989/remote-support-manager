package updateui

import "testing"

func TestReleaseNotesAreNeverMarkup(t *testing.T) {
	md := "## Fixes\n- **Faster** screen updates\n- New `Paste as keystrokes`\n<img src=x onerror=alert(1)>\n- c\n- d\n- e"
	lines := ReleaseNoteLines(md)
	if lines[0].Kind != NoteHeading || lines[0].Text != "FIXES" {
		t.Fatalf("heading: %+v", lines[0])
	}
	if lines[1].Text != "Faster screen updates" || lines[2].Text != "New Paste as keystrokes" {
		t.Fatalf("inline markers: %+v %+v", lines[1], lines[2])
	}
	if lines[3].Kind != NoteText || lines[3].Text != "<img src=x onerror=alert(1)>" {
		t.Fatalf("html must stay literal text: %+v", lines[3])
	}
	h, more := Highlights(lines, 4)
	if len(h) != 4 || !more {
		t.Fatalf("highlights: %d %v", len(h), more)
	}
	h, more = Highlights(ReleaseNoteLines("- one\n- two"), 4)
	if len(h) != 2 || more {
		t.Fatalf("short notes: %d %v", len(h), more)
	}
}
