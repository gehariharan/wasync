//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/datadir"
	"github.com/gehariharan/wasync/internal/localfs"
)

// startupData resolves the data folder as run does and opens it.
func startupData(t *testing.T, dataDir string) (config.Config, string) {
	t.Helper()
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	if dataDir != "" {
		cfg.DataDir = dataDir
	}
	if cfg, err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg, logFile, log, err := openData(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("wasync app starting")
	if err = logFile.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, "desktop.log"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, string(b)
}

func TestStartupMovesTheOldFolderBeforeItsLog(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("macOS keeps its data where earlier versions did")
	}
	home := t.TempDir()
	local, roaming := filepath.Join(home, "Local"), filepath.Join(home, "Roaming")
	t.Setenv("WASYNC_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)
	legacy, dir := filepath.Join(roaming, "wasync"), filepath.Join(local, "wasync")
	if err := localfs.EnsureDir(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "api.token"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An earlier version of the app left its log in the old folder.
	if err := os.WriteFile(filepath.Join(legacy, "desktop.log"), []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, log := startupData(t, "")
	if cfg.DataDir != dir {
		t.Fatalf("the app must use the new folder, not %s", cfg.DataDir)
	}
	if !strings.HasPrefix(log, "earlier\n") || !strings.Contains(log, "wasync moved your data to "+dir) || !strings.Contains(log, "wasync app starting") {
		t.Fatalf("the moved log must go on, noting the move: %q", log)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "api.token")); err != nil || string(b) != "kept" {
		t.Fatalf("the data must be in the new folder: %q %v", b, err)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old folder must leave %%APPDATA%%: %v", err)
	}
	if !datadir.HoldsData(filepath.Join(local, "wasync.moved-"+time.Now().Format("2006-01-02"))) {
		t.Fatal("the old folder must be kept in %LOCALAPPDATA%")
	}
}

func TestStartupLeavesAnOwnDataFolderAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mine")
	cfg, log := startupData(t, dir)
	if cfg.DataDir != dir || !strings.Contains(log, "wasync app starting") || strings.Contains(log, "data folder") {
		t.Fatalf("an own folder is used as it is: %s %q", cfg.DataDir, log)
	}
}
