// Package datadir decides where wasync keeps its data on each operating
// system, and moves the data once from where earlier versions kept it.
//
// Everything wasync keeps (the archive, the WhatsApp link, media, settings
// and credentials) lives in one private folder for each user:
//
//	macOS    ~/Library/Application Support/wasync
//	Windows  %LOCALAPPDATA%\wasync (the Microsoft Store edition: %USERPROFILE%\.wasync)
//	Linux    $XDG_DATA_HOME/wasync, normally ~/.local/share/wasync
//
// Earlier versions used %APPDATA%\wasync on Windows, which Windows copies
// between computers on managed profiles, and $XDG_CONFIG_HOME/wasync on
// Linux, which is meant for settings rather than data. Move copies such a
// folder to the new place, checks the copy and only then switches to it; the
// old folder is kept under a new name with a note saying where the data went.
package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/processlock"
)

// Name is the data folder's name inside the per-user base folder.
const Name = "wasync"

// NoteFile is the note left in a folder the data was moved out of. A folder
// that has it is never used again.
const NoteFile = "MOVED-TO-NEW-FOLDER.txt"

// Location is where wasync keeps its data by default on this computer.
type Location struct {
	// Dir is the standard per-user data folder.
	Dir string
	// Legacy is where earlier versions kept the data, or "" when they used Dir too.
	Legacy string
}

// System is the default location for this computer and build.
func System() (Location, error) {
	return Standard(runtime.GOOS, buildinfo.Distribution, os.Getenv)
}

// Standard is the default location for an operating system (a GOOS value)
// and distribution, reading the environment through getenv.
func Standard(goos, distribution string, getenv func(string) string) (Location, error) {
	switch goos {
	case "darwin":
		home := getenv("HOME")
		if home == "" {
			return Location{}, errors.New("$HOME is not defined")
		}
		return Location{Dir: filepath.Join(home, "Library", "Application Support", Name)}, nil
	case "windows":
		if distribution == "msix" {
			// The Store edition keeps a folder outside the package's own app
			// data, which Windows deletes on uninstall and hides from other
			// programs (see docs/windows.md).
			profile := getenv("USERPROFILE")
			if profile == "" {
				return Location{}, errors.New("%USERPROFILE% is not defined")
			}
			return Location{Dir: filepath.Join(profile, ".wasync")}, nil
		}
		local := getenv("LOCALAPPDATA")
		if local == "" {
			return Location{}, errors.New("%LOCALAPPDATA% is not defined")
		}
		return withLegacy(filepath.Join(local, Name), getenv("APPDATA")), nil
	}
	home := getenv("HOME")
	data := getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		if home == "" {
			return Location{}, errors.New("neither $XDG_DATA_HOME nor $HOME are defined")
		}
		data = filepath.Join(home, ".local", "share")
	}
	// Earlier versions used os.UserConfigDir, which refuses a relative
	// $XDG_CONFIG_HOME; they kept nothing anywhere then.
	config := getenv("XDG_CONFIG_HOME")
	switch {
	case config == "" && home != "":
		config = filepath.Join(home, ".config")
	case !filepath.IsAbs(config):
		config = ""
	}
	return withLegacy(filepath.Join(data, Name), config), nil
}

func withLegacy(dir, legacyBase string) Location {
	l := Location{Dir: dir}
	if legacyBase != "" {
		if legacy := filepath.Join(legacyBase, Name); !same(legacy, dir) {
			l.Legacy = legacy
		}
	}
	return l
}

// Names reports whether dir is one of the default folders, new or old. A
// login item or script made by an earlier version names the old one.
func (l Location) Names(dir string) bool {
	return l.Dir != "" && (same(dir, l.Dir) || (l.Legacy != "" && same(dir, l.Legacy)))
}

// InUse is the folder wasync uses now: Dir, unless only the old folder holds
// data, which then stays in use until Move has moved it.
func (l Location) InUse() string {
	if l.Legacy != "" && !HoldsData(l.Dir) && HoldsData(l.Legacy) && !moved(l.Legacy) {
		return l.Legacy
	}
	return l.Dir
}

// HoldsData reports whether dir exists and holds anything but lock files and
// the record of who holds them.
func HoldsData(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !housekeeping(e.Name()) {
			return true
		}
	}
	return false
}

// housekeeping names the files in the data folder that are not data: lock
// files, which wasync makes again whenever it needs them, the record of
// which process holds one, and files the file manager leaves.
func housekeeping(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case processlock.OwnerFile, ".ds_store", "desktop.ini", "thumbs.db":
		return true
	}
	return strings.HasSuffix(name, ".lock")
}

// moved reports whether the data was moved out of dir.
func moved(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, NoteFile))
	return err == nil
}

func same(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
