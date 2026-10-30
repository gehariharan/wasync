// Package service starts wasync by itself when the person logs in, so capture
// and approvals keep working without a terminal. It installs for the current
// user only and never needs administrator rights.
package service

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Spec says what to start at login.
type Spec struct {
	Exe     string // absolute path to wasync
	DataDir string // absolute path to the data directory
}

func (s Spec) validate() error {
	if !filepath.IsAbs(s.Exe) || !filepath.IsAbs(s.DataDir) {
		return fmt.Errorf("the program and data folder must be absolute paths")
	}
	return nil
}

// ErrUnsupported means this operating system has no per-user login item here.
var ErrUnsupported = errors.New("starting wasync at login is only set up for macOS and Windows")

// Manager installs or removes the login item.
type Manager interface {
	// Where names the login item in words ("a macOS login item").
	Where() string
	Install(Spec) error
	Uninstall() error
	Installed() (bool, error)
}
