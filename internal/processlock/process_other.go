//go:build !darwin && !linux && !windows

package processlock

import "fmt"

// Alive is unsupported on this platform.
func Alive(int) bool { return false }

// Terminate is unsupported on this platform.
func Terminate(int) error { return fmt.Errorf("Phase 1 supports macOS and Linux only") }
