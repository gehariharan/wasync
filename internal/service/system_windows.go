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

// valueName is the Run value wasync owns. Tests replace it so they never touch a real one.
var valueName = RunName

type runValue struct{}

// System is a value under the current user's Run key. CI writes, reads and
// removes one under a test-only name; starting wasync at login has not been
// run on a real Windows computer.
func System() Manager { return runValue{} }

func (runValue) Where() string { return "a Windows startup item" }

func (runValue) Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, RunCommand(s))
}

func (runValue) Uninstall() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err = k.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// Start runs wasync serve now, hidden and detached, as the login item would.
// Windows keeps no copy running for it, so the caller stops any running
// wasync first.
func (runValue) Start(s Spec) error {
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

func (runValue) Installed() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer k.Close()
	if _, _, err = k.GetStringValue(valueName); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
