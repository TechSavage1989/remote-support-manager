package main

import "testing"

// A clean checkout has no embedded viewer; the installer must notice
// rather than write an empty or non-executable file.
func TestViewerBytesRejectsMissingPayload(t *testing.T) {
	if b := viewerBytes(); b != nil && (b[0] != 'M' || b[1] != 'Z') {
		t.Fatal("accepted a payload that is not a Windows executable")
	}
}
