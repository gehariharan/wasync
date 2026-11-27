//go:build !darwin && !windows

// Command wasync-desktop is the wasync app for macOS and Windows. On other
// systems wasync runs from a terminal: see wasync setup.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "The wasync app runs on macOS and Windows. Here, run wasync setup in a terminal.")
	os.Exit(1)
}
