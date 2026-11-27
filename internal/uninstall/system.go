package uninstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/update"
)

// pollEvery is how often the helper looks again while it waits.
const pollEvery = 250 * time.Millisecond

// DefaultSystem is the real System. loginItems are the start-at-login items to
// remove; wasync passes the app's own (service.App) and the `wasync service
// install` one (service.System).
func DefaultSystem(loginItems ...service.Manager) System {
	return System{
		WaitOwner:      waitOwner,
		WaitExit:       waitExit,
		LoginItems:     loginItems,
		ForgetApproval: forgetApproval,
		AxiCredential:  func() (string, error) { return axicred.Path(os.Getenv) },
		RemoveAll:      os.RemoveAll,
		Trash:          trash,
		Run:            update.ExecRunner,
		Launch:         launchProgram,
	}
}

// waitOwner takes the data directory's lock once the running wasync has let
// go of it, the way the update helper does. A data directory that is already
// gone has no wasync to wait for.
func waitOwner(ctx context.Context, dataDir string) (func(), error) {
	if _, err := os.Stat(dataDir); errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	lockPath := filepath.Join(dataDir, "device.lock")
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
		case <-time.After(pollEvery):
		}
	}
}

// waitExit returns once process pid has exited.
func waitExit(ctx context.Context, pid int) error {
	for processlock.Alive(pid) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
	return nil
}
