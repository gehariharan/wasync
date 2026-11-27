package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/datadir"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/update"
)

// errDataMoved means another wasync moved the data to the new default folder
// while this command was starting with the old one.
var errDataMoved = errors.New("wasync moved its data to a new folder while this command started; run it again")

// acquireData takes the data directory's device lock. It refuses a default
// folder that is no longer the one in use, as when another wasync moved the
// data meanwhile, so nothing writes to the copy left behind.
func acquireData(dir string) (*processlock.Lock, error) {
	lock, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		return nil, err
	}
	if config.Resolve(dir) != dir {
		return nil, errors.Join(errDataMoved, lock.Close())
	}
	return lock, nil
}

// updateSettle bounds how long a move waits for an update helper to record
// how the update went in the old folder, which it does right after starting
// the new version.
const updateSettle = 30 * time.Second

// MoveData moves the data out of the folder earlier versions used, once,
// when dir is a default folder (see internal/datadir), reports what it did
// to report, and returns the folder to use. When the data cannot be moved,
// the old folder stays in use as it was, and the next start tries again.
// Every program that uses the data folder calls it before opening anything
// in it. held reports that another wasync holds the old folder, which may be
// moving it: the caller must then write nothing in the folder returned, and
// can only reach that wasync.
func MoveData(ctx context.Context, dir string, report io.Writer) (use string, held bool) {
	loc, err := datadir.System()
	if err != nil || loc.Legacy == "" || !loc.Names(dir) {
		return dir, false
	}
	for deadline := time.Now().Add(updateSettle); update.Pending(loc.Legacy) && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return config.Resolve(dir), false
		case <-time.After(250 * time.Millisecond):
		}
	}
	if update.Pending(loc.Legacy) {
		return config.Resolve(dir), false
	}
	moved, err := loc.Move(ctx, time.Now())
	switch {
	case errors.Is(err, processlock.ErrBusy):
		held = true
	case err != nil:
		fmt.Fprintf(report, "wasync could not move your data to %s, so it still uses %s: %v\n", loc.Dir, loc.Legacy, err)
	case moved != nil && !moved.Copied:
		fmt.Fprintf(report, "The old wasync folder is now kept as %s; delete it once everything works.\n", moved.Kept)
	case moved != nil && moved.Kept != "":
		fmt.Fprintf(report, "wasync moved your data to %s. The old folder is kept as %s; delete it once everything works.\n", moved.To, moved.Kept)
	case moved != nil:
		fmt.Fprintf(report, "wasync moved your data to %s. The old folder %s is no longer used; delete it once everything works.\n", moved.To, moved.From)
	}
	if moved != nil && moved.NoteErr != nil {
		old := moved.Kept
		if old == "" {
			old = moved.From
		}
		fmt.Fprintf(report, "wasync could not leave a note in the old folder %s saying where your data went (%v). It still holds a copy of your data; delete it once everything works.\n", old, moved.NoteErr)
	}
	if moved != nil && moved.Kept != "" && loc.KeepIn != "" && filepath.Dir(moved.Kept) != filepath.Clean(loc.KeepIn) {
		fmt.Fprintf(report, "wasync could not move the old folder into %s, so Windows may still copy it between computers; delete %s once everything works.\n", loc.KeepIn, moved.Kept)
	}
	return config.Resolve(dir), held
}
