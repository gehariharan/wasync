package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gehariharan/wasync/internal/config"
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

// runService adds or removes the login item. Adding one leaves a program that
// starts on every login, so it is a human choice: a typed word, then the system
// prompt where this computer has one.
func (a *App) runService(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: wasync service install|uninstall|status")
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
		return err
	case "uninstall":
		if err := mgr.Uninstall(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(a.Out, "wasync will no longer start by itself when you log in.")
		return err
	case "install":
		if !a.Interactive {
			return fmt.Errorf("service install requires an interactive terminal; nothing installed")
		}
		spec, err := a.serviceSpec(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "This adds %s that starts wasync every time you log in:\n  %s --data-dir %s serve\nIt runs in the background with your data until you remove it (wasync service uninstall).\nType INSTALL then Enter to continue: ", mgr.Where(), spec.Exe, spec.DataDir)
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
		_, err = fmt.Fprintln(a.Out, "Installed. wasync will start by itself the next time you log in.")
		return err
	}
	return fmt.Errorf("usage: wasync service install|uninstall|status")
}
