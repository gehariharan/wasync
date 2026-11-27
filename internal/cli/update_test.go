package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/update"
)

type noLoginItem struct{}

func (noLoginItem) Where() string              { return "a test login item" }
func (noLoginItem) Install(service.Spec) error { return nil }
func (noLoginItem) Uninstall() error           { return nil }
func (noLoginItem) Installed() (bool, error)   { return true, nil }

func TestAnUpdateStartsWasyncAgainTheWayItWasStarted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	exe := filepath.Join(t.TempDir(), "wasync")
	cfg := config.Config{DataDir: dir}
	app := &App{Service: noLoginItem{}, Executable: func() (string, error) { return exe, nil }, runArgs: []string{"--data-dir", dir, "serve"}}
	mac := update.Channel{Kind: update.KindMacApp, OneClick: true, Target: "/Applications/wasync.app"}

	app.ownerRec = processlock.Owner{Kind: "serve", StartedBy: "login item"}
	if r := app.relaunchFor(cfg, exe, mac); r.How != update.RelaunchLoginItem || r.ServiceExe != exe {
		t.Fatalf("login item: %+v", r)
	}
	app.ownerRec = processlock.Owner{Kind: "serve", StartedBy: "manually"}
	if r := app.relaunchFor(cfg, exe, mac); r.How != update.RelaunchCommand || r.Exe != exe || !reflect.DeepEqual(r.Args, app.runArgs) {
		t.Fatalf("serve: %+v", r)
	}
	app.ownerRec = processlock.Owner{Kind: "tui"}
	if r := app.relaunchFor(cfg, exe, mac); r.How != update.RelaunchApp || r.App != mac.Target {
		t.Fatalf("app: %+v", r)
	}
	if r := app.relaunchFor(cfg, exe, update.Channel{Kind: update.KindWindowsSetup}); r.How != update.RelaunchNone {
		t.Fatalf("terminal UI: %+v", r)
	}
}

func TestTheHelperRefusesToRunWithoutAPlan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	app := &App{}
	err := app.Run(context.Background(), []string{"--data-dir", dir, updateApplyCommand})
	if err == nil || !strings.Contains(err.Error(), "no update is waiting") {
		t.Fatalf("err %v", err)
	}
	if err := app.Run(context.Background(), []string{"--data-dir", dir, updateApplyCommand, "extra"}); err == nil {
		t.Fatal("extra arguments must be refused")
	}
}
