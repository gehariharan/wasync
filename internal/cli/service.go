package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
)

// serviceSpec records the path wasync was started by, not where a symlink
// points: a package manager's link such as /opt/homebrew/bin/wasync stays
// valid across upgrades, while the versioned folder behind it is deleted.
func (a *App) serviceSpec(cfg config.Config) (service.Spec, error) {
	executable := a.Executable
	if executable == nil {
		executable = os.Executable
	}
	exe, err := executable()
	if err != nil {
		return service.Spec{}, err
	}
	dir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return service.Spec{}, err
	}
	return service.Spec{Exe: exe, DataDir: dir}, nil
}

const serviceUsage = "usage: wasync service install|restart|uninstall|status"

// runService adds, starts or removes the login item. Adding one leaves a
// program that starts on every login, so it is a human choice: a typed word,
// then the system prompt where this computer has one. Installing also starts
// it now unless another wasync is already running from a terminal.
func (a *App) runService(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 {
		return errors.New(serviceUsage)
	}
	mgr := a.Service
	if mgr == nil || mgr.Where() == "" {
		return service.ErrUnsupported
	}
	switch args[0] {
	case "status":
		on, err := mgr.Installed()
		if err != nil {
			return err
		}
		state := "not installed"
		if on {
			state = "installed"
		}
		_, err = fmt.Fprintf(a.Out, "start at login: %s (%s)\n", state, mgr.Where())
		if err == nil && on {
			switch owner, ok := liveOwner(cfg); {
			case ok && owner.StartedBy == "login item":
				_, err = fmt.Fprintln(a.Out, "running in the background: yes")
			case ok && owner.StartedBy == "":
				_, err = fmt.Fprintln(a.Out, "running: yes, but wasync cannot tell whether in the background (to be sure, run: wasync service restart)")
			default:
				_, err = fmt.Fprintln(a.Out, "running in the background: no (start it with: wasync service restart)")
			}
		}
		return err
	case "uninstall":
		if err := mgr.Uninstall(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(a.Out, "wasync will no longer start by itself when you log in.")
		return err
	case "restart":
		return a.serviceStart(ctx, cfg, mgr, true)
	case "install":
		if !a.Interactive {
			return fmt.Errorf("service install requires an interactive terminal; nothing installed")
		}
		spec, err := a.serviceSpec(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "This adds %s that starts wasync now and every time you log in:\n  %s --data-dir %s serve\nIt runs in the background with your data until you remove it (wasync service uninstall).\nType INSTALL then Enter to continue: ", mgr.Where(), spec.Exe, spec.DataDir)
		line, err := readLine(ctx, a.In)
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) != "INSTALL" {
			return fmt.Errorf("confirmation did not match; nothing installed")
		}
		if err = a.confirmHuman(ctx, "Approve: start wasync by itself every time you log in, and keep it running in the background"); err != nil {
			return fmt.Errorf("%w; nothing installed", err)
		}
		if err = mgr.Install(spec); err != nil {
			if errors.Is(err, service.ErrUnsupported) {
				return err
			}
			return fmt.Errorf("could not install: %w", err)
		}
		fmt.Fprintln(a.Out, "Installed. wasync starts by itself every time you log in.")
		return a.serviceStart(ctx, cfg, mgr, false)
	}
	return errors.New(serviceUsage)
}

// serviceStart starts the login item's wasync now, so it does not wait for
// the next login. A copy the login item started before is stopped first, so
// an upgraded program takes over. Another wasync, such as one running in a
// terminal, is stopped too when takeOver is set (wasync service restart), and
// otherwise left running.
func (a *App) serviceStart(ctx context.Context, cfg config.Config, mgr service.Manager, takeOver bool) error {
	spec, err := a.serviceSpec(cfg)
	if err != nil {
		return err
	}
	if on, err := mgr.Installed(); err != nil {
		return err
	} else if !on {
		return service.ErrNotInstalled
	}
	if _, isStarter := mgr.(service.Starter); !isStarter {
		if takeOver {
			return service.ErrUnsupported
		}
		_, err = fmt.Fprintln(a.Out, "It starts at your next login.")
		return err
	}
	if owner, running := liveOwner(cfg); running && owner.StartedBy != "login item" && !takeOver {
		if owner.StartedBy == "" {
			_, err = fmt.Fprintf(a.Out, "%s is running now, so it was left alone; wasync cannot tell whether it is the background copy. To be sure the background copy runs this version, run: %s\n", describeOwner(owner), commandFor(cfg, "service restart"))
			return err
		}
		_, err = fmt.Fprintf(a.Out, "%s is running now, so it was left alone. To run wasync in the background instead, run: %s\n", describeOwner(owner), commandFor(cfg, "service restart"))
		return err
	}
	held, err := processlock.Held(filepath.Join(cfg.DataDir, "device.lock"))
	if err != nil {
		return err
	}
	if held {
		if _, stopped, err := a.stopOwner(ctx, cfg); err != nil {
			return err
		} else if !stopped {
			return fmt.Errorf("another wasync command is using the data directory; try %q again in a moment", commandFor(cfg, "service restart"))
		}
	}
	if err = service.Start(mgr, spec); err != nil {
		return fmt.Errorf("could not start wasync in the background: %w; it starts at your next login", err)
	}
	_, err = fmt.Fprintf(a.Out, "Started. wasync runs in the background now (log: %s).\n", filepath.Join(spec.DataDir, "service.log"))
	return err
}
