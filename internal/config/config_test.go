package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gehariharan/wasync/internal/buildinfo"
)

// isolate points every per-user base folder at a fresh temporary home and
// returns the standard data folder and the old one ("" where there is none).
func isolate(t *testing.T) (dir, legacy string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WASYNC_DATA_DIR", "")
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", home)
		return filepath.Join(home, "Library", "Application Support", "wasync"), ""
	case "windows":
		t.Setenv("USERPROFILE", home)
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		return filepath.Join(home, "AppData", "Local", "wasync"), filepath.Join(home, "AppData", "Roaming", "wasync")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	return filepath.Join(home, ".local", "share", "wasync"), filepath.Join(home, ".config", "wasync")
}

func holdData(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "archive.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func defaultDir(t *testing.T) string {
	t.Helper()
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.DataDir
}

func TestDefaultDataDirectoryPerSystem(t *testing.T) {
	dir, _ := isolate(t)
	if got := defaultDir(t); got != dir {
		t.Fatalf("default %q, want %q", got, dir)
	}
	if runtime.GOOS == "windows" {
		original := buildinfo.Distribution
		defer func() { buildinfo.Distribution = original }()
		buildinfo.Distribution = "msix"
		want := filepath.Join(os.Getenv("USERPROFILE"), ".wasync")
		if got := defaultDir(t); got != want {
			t.Fatalf("Store default %q, want %q", got, want)
		}
	}
}

func TestTheOldFolderStaysInUseUntilItsDataMoves(t *testing.T) {
	dir, legacy := isolate(t)
	if legacy == "" {
		t.Skip("this system keeps its data where earlier versions did")
	}
	holdData(t, legacy)
	if got := defaultDir(t); got != legacy {
		t.Fatalf("with data only in the old folder the default is %q, want %q", got, legacy)
	}
	// A login item from an earlier version names the old folder, a person may
	// name the new one: both mean the folder in use.
	for _, named := range []string{legacy, dir} {
		t.Setenv("WASYNC_DATA_DIR", named)
		if got := defaultDir(t); got != legacy {
			t.Fatalf("WASYNC_DATA_DIR=%s gave %q, want %q", named, got, legacy)
		}
		if cfg, err := (Config{DataDir: named, Timeout: 1}).Validate(); err != nil || cfg.DataDir != legacy {
			t.Fatalf("--data-dir %s gave %+v %v", named, cfg, err)
		}
	}
	holdData(t, dir)
	t.Setenv("WASYNC_DATA_DIR", legacy)
	if got := defaultDir(t); got != dir {
		t.Fatalf("after the move the old folder's name must mean the new one, got %q", got)
	}
}

func TestExplicitDataDirectoryIsUntouched(t *testing.T) {
	_, legacy := isolate(t)
	if legacy != "" {
		holdData(t, legacy)
	}
	explicit := filepath.Join(t.TempDir(), "explicit")
	t.Setenv("WASYNC_DATA_DIR", explicit)
	if got := defaultDir(t); got != explicit {
		t.Fatalf("explicit override lost: %q", got)
	}
	other := filepath.Join(t.TempDir(), "flag")
	if cfg, err := (Config{DataDir: other, Timeout: 1}).Validate(); err != nil || cfg.DataDir != other {
		t.Fatalf("explicit flag lost: %+v %v", cfg, err)
	}
}
