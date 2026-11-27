//go:build windows

package uninstall

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// helperExe copies wasync.exe into the helper's temporary folder and returns
// the copy: Windows keeps a running program's file locked, and the uninstaller
// must delete it.
func helperExe(exe, dir string) (string, error) {
	dst := filepath.Join(dir, "wasync-remove.exe")
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

// startDetached starts the helper with no console window, in dir, so it
// keeps running after this wasync exits. Its working folder matters: Windows
// cannot delete a folder a running program works in, and wasync often runs
// in the folder the uninstaller removes. Its output is appended to outPath.
func startDetached(exe string, args []string, dir, outPath string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
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

// launchProgram starts a program, with its own window, and does not wait for
// it. It works in the temporary folder, not the install folder, for the same
// reason as startDetached.
func launchProgram(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
