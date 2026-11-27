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

// A wasync the app started has no standard input after an update, so the
// update opens the app again instead, and the app starts its own wasync.
func TestAnUpdateOpensTheAppAgainForAWasyncTheAppStarted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	exe := filepath.Join(t.TempDir(), "wasync")
	desktop := filepath.Join(t.TempDir(), "wasync-desktop.exe")
	app := &App{Service: noLoginItem{}, Getenv: func(k string) string { return map[string]string{DesktopAppEnv: desktop}[k] },
		runArgs: []string{"--data-dir", dir, "setup", "--app"}, ownerRec: processlock.Owner{Kind: "setup", StartedBy: startedByApp}}
	mac := update.Channel{Kind: update.KindMacApp, OneClick: true, Target: "/Applications/wasync.app"}
	if r := app.relaunchFor(config.Config{DataDir: dir}, exe, mac); r.How != update.RelaunchApp || r.App != mac.Target {
		t.Fatalf("macOS app: %+v", r)
	}
	windows := update.Channel{Kind: update.KindWindowsSetup, OneClick: true, Target: exe}
	if r := app.relaunchFor(config.Config{DataDir: dir}, exe, windows); r.How != update.RelaunchCommand || r.Exe != desktop || len(r.Args) != 0 {
		t.Fatalf("Windows app: %+v", r)
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
