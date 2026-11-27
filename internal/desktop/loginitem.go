package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
)

// Before the wasync app, `wasync service install` made a login item that
// starts `wasync serve` at login. The app's own item ("Start when I log in")
// does that job now by opening the app, which starts wasync itself. With both,
// whichever starts first holds the data directory and the other cannot run, so
// when the app starts and finds the old item for its own data directory it
// moves the person to its own item. They already chose to start at login, so
// it asks nothing. An old item for another data directory is left alone.

// freeWait bounds how long the app waits for the wasync serve the old item
// started to let go of the data directory.
const freeWait = 30 * time.Second

// loginItemMove is one move from the old item to the app's own.
type loginItemMove struct {
	// legacy is the `wasync service install` item, app the app's own.
	legacy, app service.Manager
	// spec is what the app's item opens: the app, for its data directory.
	spec service.Spec
	// stop stops the wasync serve the old item started, where removing the
	// item does not (Windows). Nil where it does (macOS).
	stop func() error
	log  *slog.Logger
}

// run makes the move when the old item is installed for the same data
// directory, and reports whether it did. The app's item is installed before
// the old one goes, so a failure never leaves the person without either.
func (m loginItemMove) run() (bool, error) {
	on, err := m.legacy.Installed()
	if err != nil {
		return false, fmt.Errorf("check the wasync serve login item: %w", err)
	}
	if !on {
		return false, nil
	}
	old, err := service.Read(m.legacy)
	if err != nil {
		return false, fmt.Errorf("read the wasync serve login item: %w", err)
	}
	if !sameDataDir(old.DataDir, m.spec.DataDir) {
		m.log.Info("left the wasync serve login item alone: it is for another data directory", "its_data_dir", old.DataDir, "data_dir", m.spec.DataDir)
		return false, nil
	}
	have, err := m.app.Installed()
	if err != nil {
		return false, fmt.Errorf("check the app's login item: %w", err)
	}
	if !have {
		if err := m.app.Install(m.spec); err != nil {
			return false, fmt.Errorf("install the app's login item, so the wasync serve one was kept: %w", err)
		}
		m.log.Info("the wasync app now starts at login in place of the wasync serve login item", "app", m.spec.Exe)
	}
	if err := m.legacy.Uninstall(); err != nil {
		return false, fmt.Errorf("remove the wasync serve login item: %w", err)
	}
	m.log.Info("removed the wasync serve login item", "program", old.Exe, "data_dir", old.DataDir)
	if m.stop != nil {
		if err := m.stop(); err != nil {
			m.log.Warn("stopping the wasync serve the old login item started", "error", err)
		}
	}
	return true, nil
}

// sameDataDir compares data directories as wasync resolves them, so an old
// item naming the folder earlier versions used still counts as this one.
func sameDataDir(a, b string) bool {
	a, b = filepath.Clean(config.Resolve(a)), filepath.Clean(config.Resolve(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// startedByLegacyItem reports whether a running wasync looks like the one the
// old item started. Windows does not record what started a wasync serve, so
// there any wasync serve that does not say it was started by hand counts.
func startedByLegacyItem(kind, startedBy string) bool {
	return kind == "serve" && (startedBy == "login item" || startedBy == "")
}

// MoveLoginItem moves the person from the `wasync service install` login item
// (legacy, service.System) to the app's own (app, service.App) when the old one
// starts wasync for the app's data directory. It then waits, briefly, for the
// wasync serve the old item started to stop, so the app can start its own:
// removing the item stops it on macOS, and on Windows it is asked to stop
// cleanly, as `wasync stop` does.
func (h *Host) MoveLoginItem(ctx context.Context, legacy, app service.Manager, log *slog.Logger) (bool, error) {
	st := h.CLI.Status(ctx, h.config())
	running := st.Running && startedByLegacyItem(st.Kind, st.StartedBy)
	m := loginItemMove{legacy: legacy, app: app, spec: service.Spec{Exe: h.App, DataDir: h.DataDir()}, log: log}
	if running && runtime.GOOS == "windows" {
		m.stop = h.stopOther
	}
	moved, err := m.run()
	if moved && running {
		h.waitFree(ctx, freeWait)
	}
	return moved, err
}

// stopOther asks the wasync that holds the data directory to stop cleanly, by
// running `wasync stop`, which bounds its own wait.
func (h *Host) stopOther() error {
	cmd := h.command(h.Wasync, "--data-dir", h.DataDir(), "--timeout", "45s", "stop")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitFree waits up to wait for no wasync to hold the data directory.
func (h *Host) waitFree(ctx context.Context, wait time.Duration) {
	lock := filepath.Join(h.DataDir(), "device.lock")
	until := time.Now().Add(wait)
	for time.Now().Before(until) {
		if held, err := processlock.Held(lock); err != nil || !held {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
