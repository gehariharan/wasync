//go:build windows

package processlock

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Alive reports whether a process with this id exists.
func Alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// Terminate is not offered here: Windows has no clean shutdown request for a
// console program, so the caller says to end the process instead.
func Terminate(pid int) error {
	return fmt.Errorf("Windows cannot ask process %d to stop cleanly", pid)
}
