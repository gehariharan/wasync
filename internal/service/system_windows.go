//go:build windows

package service

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// valueName and appValueName are the Run values wasync owns. Tests replace
// them so they never touch a real one.
var (
	valueName    = RunName
	appValueName = AppRunName
)

// runValue is a value under the current user's Run key: name says which one,
// and command what it runs.
type runValue struct {
	name    *string
	command func(Spec) string
}

// System is a value under the current user's Run key. CI writes, reads and
// removes one under a test-only name; starting wasync at login has not been
// run on a real Windows computer.
func System() Manager { return serveRun{runValue{name: &valueName, command: RunCommand}} }

// App is the Run value that opens the wasync app, with no window, at login.
func App() Manager { return runValue{name: &appValueName, command: AppRunCommand} }

func (runValue) Where() string { return "a Windows startup item" }

func (v runValue) Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(*v.name, v.command(s))
}

func (v runValue) Uninstall() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err = k.DeleteValue(*v.name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func (v runValue) Installed() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer k.Close()
	if _, _, err = k.GetStringValue(*v.name); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// serveRun is the Run value that starts `wasync serve`; it can also start it now.
type serveRun struct{ runValue }

// Start runs wasync serve now, hidden and detached, as the login item would.
// Windows keeps no copy running for it, so the caller stops any running
// wasync first.
func (serveRun) Start(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	cmd := exec.Command(s.Exe, "--data-dir", s.DataDir, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
