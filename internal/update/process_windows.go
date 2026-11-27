//go:build windows

package update

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// writable is not used on Windows: Setup installs per user, where the user
// may always write.
func writable(string) bool { return true }

// helperExe copies wasync.exe into the update folder and returns the copy:
// Windows keeps a running program's file locked, and Setup must replace it.
func helperExe(exe, dataDir string) (string, error) {
	dir, err := os.MkdirTemp(Folder(dataDir), "helper-*")
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "wasync-update.exe")
	in, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
}

// StartDetached starts a program with no console window that keeps running
// after this wasync exits. Its output is appended to logPath.
func StartDetached(exe string, args []string, logPath string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
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
