//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the viewer installer runs on Windows only")
	if viewerBytes() == nil {
		fmt.Fprintln(os.Stderr, "(and this build does not embed the viewer)")
	}
	os.Exit(1)
}
