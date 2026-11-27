package cli

import (
	"context"
	"fmt"

	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/uninstall"
)

// removeApplyCommand is the hidden command the running wasync starts, detached,
// to finish removing wasync once it has stopped (see internal/uninstall).
const removeApplyCommand = uninstall.ApplyCommand

// runRemoveApply is the helper: it waits for the running wasync and the wasync
// app to stop, removes the login items, deletes the data when the person
// asked for that, and hands over to the platform's own uninstall.
func (a *App) runRemoveApply(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: wasync %s REQUEST_FILE (started by wasync itself)", removeApplyCommand)
	}
	sys := uninstall.DefaultSystem(a.removeLoginItems()...)
	if a.RemoveSystem != nil {
		sys = *a.RemoveSystem
	}
	return uninstall.Apply(ctx, args[0], sys)
}

// removeLoginItems are both start-at-login items wasync may have made: the
// wasync app's own, and the `wasync service install` one from before the app.
func (a *App) removeLoginItems() []service.Manager {
	app := a.AppService
	if app == nil {
		app = service.App()
	}
	items := []service.Manager{app}
	if a.Service != nil {
		items = append(items, a.Service)
	}
	return items
}
