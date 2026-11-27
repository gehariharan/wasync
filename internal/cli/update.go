package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/update"
)

// updateApplyCommand is the hidden command the running wasync starts, detached,
// to install a checked update once it has stopped (see internal/update).
const updateApplyCommand = "update-apply"

// updater is what the owner's web app uses to check for and install updates.
func (a *App) updater(ctx context.Context, cfg config.Config, stop func()) *update.Manager {
	executable := a.Executable
	if executable == nil {
		executable = os.Executable
	}
	exe, err := executable()
	if err != nil {
		return nil
	}
	ch := update.Detect(update.ThisHost(exe, buildinfo.Version, buildinfo.Distribution))
	source, platform := a.UpdateSource, a.UpdatePlatform
	if source == nil {
		source = update.GitHub{}
	}
	if platform == nil {
		platform = update.DefaultPlatform()
	}
	launch := a.UpdateLaunch
	if launch == nil {
		launch = func(dataDir string) error { return update.LaunchHelper(exe, dataDir) }
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil
	}
	return update.NewManager(ctx, update.Config{
		DataDir: dataDir, Current: buildinfo.Version, Channel: ch, Source: source, Platform: platform,
		Relaunch: a.relaunchFor(cfg, exe, ch), Launch: launch, Stop: stop,
	})
}

// relaunchFor decides how wasync starts again after an update: by opening the
// wasync app that started it, through the login item that started it, as the
// same background command, by opening the macOS app, or not at all for a
// terminal UI, which needs its terminal.
func (a *App) relaunchFor(cfg config.Config, exe string, ch update.Channel) update.Relaunch {
	if desktop := a.desktopApp(); a.ownerRec.StartedBy == startedByApp && desktop != "" {
		if ch.Kind == update.KindMacApp && ch.Target != "" {
			return update.Relaunch{How: update.RelaunchApp, App: ch.Target}
		}
		return update.Relaunch{How: update.RelaunchCommand, Exe: desktop}
	}
	if a.ownerRec.StartedBy == "login item" && a.Service != nil {
		if spec, err := a.serviceSpec(cfg); err == nil {
			return update.Relaunch{How: update.RelaunchLoginItem, ServiceExe: spec.Exe, DataDir: spec.DataDir}
		}
	}
	switch a.ownerRec.Kind {
	case "serve", "setup":
		return update.Relaunch{How: update.RelaunchCommand, Exe: exe, Args: append([]string(nil), a.runArgs...)}
	}
	if ch.Kind == update.KindMacApp && ch.Target != "" {
		return update.Relaunch{How: update.RelaunchApp, App: ch.Target}
	}
	return update.Relaunch{How: update.RelaunchNone}
}

// runUpdateApply is the helper: it waits for the running wasync to stop,
// installs the update it checked, and starts wasync again.
func (a *App) runUpdateApply(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: wasync %s (started by wasync itself)", updateApplyCommand)
	}
	platform := a.UpdatePlatform
	if platform == nil {
		platform = update.DefaultPlatform()
	}
	lockPath := filepath.Join(cfg.DataDir, "device.lock")
	return update.Apply(ctx, cfg.DataDir, update.ApplyDeps{
		Platform: platform,
		WaitOwner: func(ctx context.Context) (func(), error) {
			for {
				lock, err := processlock.Acquire(lockPath)
				if err == nil {
					return func() { _ = lock.Close() }, nil
				}
				if !errors.Is(err, processlock.ErrBusy) {
					return nil, err
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
			}
		},
		Start: func(r update.Relaunch) error {
			return update.StartAgain(r, cfg.DataDir, func() error {
				if a.Service == nil {
					return service.ErrUnsupported
				}
				return service.Start(a.Service, service.Spec{Exe: r.ServiceExe, DataDir: r.DataDir})
			})
		},
	})
}
