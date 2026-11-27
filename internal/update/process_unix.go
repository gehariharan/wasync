//go:build !windows

package update

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// writable reports whether this user may create and rename entries in dir.
func writable(dir string) bool { return unix.Access(dir, unix.W_OK|unix.X_OK) == nil }

// helperExe is the program to run as the helper. On macOS and Linux the
// running file stays usable after it is replaced, so it runs in place.
func helperExe(exe, _ string) (string, error) { return exe, nil }

// StartDetached starts a program in its own session, so it keeps running
// after this wasync exits and is not stopped along with it. Its output is
// appended to logPath.
func StartDetached(exe string, args []string, logPath string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return startLogged(cmd, logPath)
}

func startLogged(cmd *exec.Cmd, logPath string) error {
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
