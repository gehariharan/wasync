package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/uninstall"
)

func TestRemoveApplyRefusesWithoutARequest(t *testing.T) {
	app := &App{AppService: &loginItem{}, Service: &loginItem{}}
	err := app.Run(context.Background(), []string{removeApplyCommand, filepath.Join(t.TempDir(), "missing.json")})
	if err == nil || !strings.Contains(err.Error(), "no removal of wasync is waiting") {
		t.Fatalf("err %v", err)
	}
	if err := app.Run(context.Background(), []string{removeApplyCommand}); err == nil {
		t.Fatal("the request file is required")
	}
}

// The helper as wasync starts it, with the real waits and the data kept: it
// waits for the data directory to be free, removes both login items, and
// leaves its log in the data directory.
func TestRemoveApplyRemovesBothLoginItemsOnceWasyncHasStopped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	// A wasync that stopped earlier leaves its lock file; it holds nothing now.
	lock, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	reqDir := filepath.Join(t.TempDir(), "wasync-remove-test")
	if err := localfs.EnsureDir(reqDir); err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(reqDir, "request.json")
	b, _ := json.Marshal(uninstall.Request{DataDir: dir, Handoff: uninstall.Handoff{Kind: uninstall.KindManual, Note: "Delete the program files."}})
	f, err := localfs.OpenFile(reqPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	f.Close()
	appItem := &loginItem{installed: true}
	serveItem := &loginItem{installed: true}
	app := &App{AppService: appItem, Service: serveItem}
	if err := app.Run(context.Background(), []string{removeApplyCommand, reqPath}); err != nil {
		t.Fatal(err)
	}
	for name, item := range map[string]*loginItem{"app": appItem, "wasync serve": serveItem} {
		if on, _ := item.Installed(); on {
			t.Errorf("the %s login item must be removed", name)
		}
	}
	logged, err := os.ReadFile(uninstall.LogPath(dir))
	if err != nil {
		t.Fatalf("the helper's log belongs in the kept data directory: %v", err)
	}
	if !strings.Contains(string(logged), "Delete the program files.") {
		t.Fatalf("the log must say what is left to do:\n%s", logged)
	}
}

func TestRemoveApplyRemovesTheServeItemOnlyWhereThereIsOne(t *testing.T) {
	appItem := &loginItem{}
	items := (&App{AppService: appItem}).removeLoginItems()
	if len(items) != 1 || items[0] != service.Manager(appItem) {
		t.Fatalf("without a serve item only the app's is removed: %v", items)
	}
	serve := &loginItem{}
	if items := (&App{AppService: appItem, Service: serve}).removeLoginItems(); len(items) != 2 || items[1] != service.Manager(serve) {
		t.Fatalf("both items: %v", items)
	}
}
