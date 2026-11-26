//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the server setup runs on Windows only; on other systems run \"rsm-server run\" and open http://127.0.0.1:8080")
	os.Exit(1)
}
