//go:build darwin

package service

import (
	"os"
	"time"
)

// System is the current user's macOS login item that starts `wasync serve`.
func System() Manager {
	home, err := os.UserHomeDir()
	if err != nil {
		return unsupported{}
	}
	return launchAgent{Home: home, Run: runCommand, Wait: time.Sleep}
}

// App is the current user's macOS login item that opens the wasync app.
func App() Manager {
	home, err := os.UserHomeDir()
	if err != nil {
		return unsupported{}
	}
	return appAgent{Home: home}
}
