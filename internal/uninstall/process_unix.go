//go:build !windows

package uninstall

import (
	"os"
	"os/exec"
	"syscall"
)

// helperExe is the program to run as the helper. On macOS and Linux a running
// program keeps working after its file is moved or deleted, so it runs in place.
func helperExe(exe, _ string) (string, error) { return exe, nil }

// startDetached starts the helper in its own session, in dir, so it keeps
// running after this wasync exits and is not stopped along with it. Its
// output is appended to outPath.
func startDetached(exe string, args []string, dir, outPath string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return startWithOutput(cmd, outPath)
}

func startWithOutput(cmd *exec.Cmd, outPath string) error {
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// launchProgram starts a program and does not wait for it.
func launchProgram(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
