//go:build darwin || linux

package processlock

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Alive reports whether a process with this id exists.
func Alive(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// Terminate asks the process to shut down cleanly, as Ctrl-C would. It is the
// way to stop an owner that has no local API.
func Terminate(pid int) error {
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		return fmt.Errorf("could not ask process %d to stop: %w", pid, err)
	}
	return nil
}
