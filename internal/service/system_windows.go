//go:build windows

package service

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

type runValue struct{}

// System is a value under the current user's Run key. This path has not been
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
	return k.SetStringValue(RunName, RunCommand(s))
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
	if err = k.DeleteValue(RunName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
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
	if _, _, err = k.GetStringValue(RunName); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
