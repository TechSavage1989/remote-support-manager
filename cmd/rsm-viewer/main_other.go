//go:build !windows

package main

import (
	"fmt"
	"os"
)

func run() {
	fmt.Fprintln(os.Stderr, "The Remote Support Manager viewer is a Windows application. Build it with GOOS=windows (see docs/BUILD.md).")
	os.Exit(1)
}
