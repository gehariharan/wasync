//go:build darwin

package service

import "os"

// System is the current user's macOS login item.
func System() Manager {
	home, err := os.UserHomeDir()
	if err != nil {
		return unsupported{}
	}
	return launchAgent{Home: home, Run: runCommand}
}
