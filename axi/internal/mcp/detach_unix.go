//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// spawnDetached starts the program in its own session so closing this tool
// server does not stop it.
func spawnDetached(bin string, args []string) error {
	return run(bin, args, func(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} })
}
