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

// ErrNotInstalled means there is no login item to start.
var ErrNotInstalled = errors.New("wasync does not start at login yet; run wasync service install")

// Manager installs or removes the login item.
type Manager interface {
	// Where names the login item in words ("a macOS login item").
	Where() string
	Install(Spec) error
	Uninstall() error
	Installed() (bool, error)
}

// Starter is a Manager that can also start its login item's wasync now,
// without waiting for the next login. Start replaces a copy it started before,
// so a new program file or setting takes effect. The caller makes sure no
// other wasync holds the data directory first.
type Starter interface {
	Start(Spec) error
}

// Start starts the login item's wasync now where m can.
func Start(m Manager, s Spec) error {
	st, ok := m.(Starter)
	if !ok {
		return ErrUnsupported
	}
	if on, err := m.Installed(); err != nil {
		return err
	} else if !on {
		return ErrNotInstalled
	}
	return st.Start(s)
}
