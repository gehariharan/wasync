// Package config resolves the shared local configuration for all commands.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/datadir"
)

type Config struct {
	DataDir            string
	Timeout            time.Duration
	APIAddr            string
	NoAPI              bool
	DownloadMedia      bool
	PairingFullHistory bool
}

// Default is the configuration before flags: WASYNC_DATA_DIR, else the
// per-user data folder in use on this computer (see internal/datadir).
func Default() (Config, error) {
	dir := os.Getenv("WASYNC_DATA_DIR")
	if dir == "" {
		loc, err := datadir.System()
		if err != nil {
			return Config{}, fmt.Errorf("resolve the data directory: %w", err)
		}
		dir = loc.InUse()
	} else {
		dir = Resolve(dir)
	}
	addr := os.Getenv("WASYNC_API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	return Config{DataDir: dir, Timeout: 3 * time.Minute, APIAddr: addr}, nil
}

// Resolve returns dir, except that a folder naming the default location, the
// old one or the new one (as a login item made by an earlier version does),
// means the default folder in use, wherever its data is now.
func Resolve(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if loc, err := datadir.System(); err == nil && loc.Names(abs) {
		return loc.InUse()
	}
	return dir
}

func (c Config) Validate() (Config, error) {
	if c.DataDir == "" {
		return c, fmt.Errorf("data directory cannot be empty")
	}
	if c.Timeout <= 0 {
		return c, fmt.Errorf("timeout must be positive")
	}
	dir, err := filepath.Abs(Resolve(c.DataDir))
	if err != nil {
		return c, fmt.Errorf("resolve data directory: %w", err)
	}
	if dir == filepath.VolumeName(dir)+string(filepath.Separator) {
		return c, fmt.Errorf("use a dedicated data directory, not the filesystem root")
	}
	c.DataDir = dir
	return c, nil
}
