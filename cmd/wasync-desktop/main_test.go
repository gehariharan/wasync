//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/datadir"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
)

// startupConfig resolves the data folder as run does.
func startupConfig(t *testing.T, dataDir string) config.Config {
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
	return cfg
}

// startupData opens the data folder as run does and returns its log.
func startupData(t *testing.T, dataDir string) (config.Config, string) {
	t.Helper()
	cfg, logFile, err := openData(context.Background(), startupConfig(t, dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if logFile == nil {
		t.Fatal("the app must open its log")
	}
	slog.New(slog.NewTextHandler(logFile, nil)).Info("wasync app starting")
	if err = logFile.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, "desktop.log"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, string(b)
}

// windowsFolders points %LOCALAPPDATA% and %APPDATA% at a temporary home
// and returns the new data folder and the old one, which holds data.
func windowsFolders(t *testing.T) (local, dir, legacy string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("macOS keeps its data where earlier versions did")
	}
	home := t.TempDir()
	local, roaming := filepath.Join(home, "Local"), filepath.Join(home, "Roaming")
	t.Setenv("WASYNC_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)
	legacy, dir = filepath.Join(roaming, "wasync"), filepath.Join(local, "wasync")
	if err := localfs.EnsureDir(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "api.token"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	return local, dir, legacy
}

func TestStartupMovesTheOldFolderBeforeItsLog(t *testing.T) {
	local, dir, legacy := windowsFolders(t)
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
	if cfg.DataDir != dir || !strings.Contains(log, "wasync app starting") || strings.Contains(log, "moved your data") {
		t.Fatalf("an own folder is used as it is: %s %q", cfg.DataDir, log)
	}
}

func TestSecondStartWhileTheOldFolderIsHeldWritesNothingThere(t *testing.T) {
	_, dir, legacy := windowsFolders(t)
	// The first copy of the app, moving the data, holds the old folder.
	lock, err := processlock.Acquire(filepath.Join(legacy, "device.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	before, err := os.ReadDir(legacy)
	if err != nil {
		t.Fatal(err)
	}
	cfg, logFile, err := openData(context.Background(), startupConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if logFile != nil {
		logFile.Close()
		t.Fatalf("no log may be opened in %s while it is held", cfg.DataDir)
	}
	after, err := os.ReadDir(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the held old folder changed: %v, then %v", before, after)
	}
	if datadir.HoldsData(dir) {
		t.Fatal("nothing may be moved while the old folder is held")
	}
}
