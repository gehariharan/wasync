package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/datadir"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

// defaultFolders points the per-user base folders at a temporary home and
// returns the standard data folder and the one earlier versions used. It
// skips where both are the same (macOS and the Store edition).
func defaultFolders(t *testing.T) (dir, legacy string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WASYNC_DATA_DIR", "")
	switch runtime.GOOS {
	case "darwin":
		t.Skip("macOS keeps its data where earlier versions did")
	case "windows":
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "Local"))
		t.Setenv("APPDATA", filepath.Join(home, "Roaming"))
	default:
		t.Setenv("HOME", home)
		t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	}
	loc, err := datadir.System()
	if err != nil || loc.Legacy == "" {
		t.Skipf("no old folder on this build: %+v %v", loc, err)
	}
	return loc.Dir, loc.Legacy
}

func TestFirstStartAfterTheUpdateMovesTheData(t *testing.T) {
	dir, legacy := defaultFolders(t)
	if err := localfs.EnsureDir(legacy); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), filepath.Join(legacy, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err = os.WriteFile(filepath.Join(legacy, "api.token"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: io.Discard, Err: &stderr,
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return &fakeClient{}, nil }}
	// The login item an earlier version made names the old folder.
	if err = app.Run(context.Background(), []string{"--data-dir", legacy, "status"}); err != nil {
		t.Fatal(err)
	}
	kept := legacy + ".moved-" + time.Now().Format("2006-01-02")
	if runtime.GOOS == "windows" {
		// Out of %APPDATA%, which roams, into %LOCALAPPDATA%.
		kept = filepath.Join(filepath.Dir(dir), "wasync.moved-"+time.Now().Format("2006-01-02"))
	}
	if !strings.Contains(stderr.String(), "wasync moved your data to "+dir) || !strings.Contains(stderr.String(), kept) {
		t.Fatalf("the move must be reported: %q", stderr.String())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "api.token")); err != nil || string(b) != "kept" {
		t.Fatalf("the data must be in the new folder: %q %v", b, err)
	}
	if _, err = os.Stat(filepath.Join(kept, datadir.NoteFile)); err != nil {
		t.Fatalf("the old folder must be kept with its note: %v", err)
	}
	// Later starts, through the old name too, use the new folder and never
	// make the old one again.
	stderr.Reset()
	for _, args := range [][]string{{"--data-dir", legacy, "status"}, {"status"}} {
		if err = app.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("nothing more to report: %q", stderr.String())
	}
	if _, err = os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old folder came back: %v", err)
	}
}

func TestMoveFailureKeepsUsingTheOldFolder(t *testing.T) {
	dir, legacy := defaultFolders(t)
	if err := localfs.EnsureDir(legacy); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), filepath.Join(legacy, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	// Something the move must not follow makes it fail.
	if err = os.Symlink(t.TempDir(), filepath.Join(legacy, "elsewhere")); err != nil {
		t.Skipf("cannot make a link here: %v", err)
	}
	var stderr bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: io.Discard, Err: &stderr,
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return &fakeClient{}, nil }}
	if err = app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "could not move your data to "+dir+", so it still uses "+legacy) {
		t.Fatalf("the failure must be reported: %q", stderr.String())
	}
	if datadir.HoldsData(dir) {
		t.Fatal("a failed move must not leave data in the new folder")
	}
	if _, err = os.Stat(filepath.Join(legacy, "archive.db")); err != nil {
		t.Fatal(err)
	}
}
