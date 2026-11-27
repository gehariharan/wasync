package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows has no Dock: the taskbar button comes and goes with the window.
func showInDock(bool) {}

func wasyncName() string { return "wasync.exe" }

// wasyncCommand starts wasync with no console window of its own.
func wasyncCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}
