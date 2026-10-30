//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// spawnDetached starts the program with no console of its own and outside this
// process group. This path has not been run on a real Windows computer.
func spawnDetached(bin string, args []string) error {
	return run(bin, args, func(c *exec.Cmd) {
		c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup, HideWindow: true}
	})
}
