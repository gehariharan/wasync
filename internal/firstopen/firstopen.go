// Package firstopen remembers whether the wasync app's First open screen has
// been shown for a data directory. The screen (a welcome, and the note that
// wasync is not WhatsApp, with "I understand") is shown once; the app reads
// Pending to decide whether to open it, and the screen calls Done when the
// person has read it.
package firstopen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// FileName marks the screen as seen; it holds the time it was.
const FileName = "first-open.done"

// Pending reports whether the First open screen is still to be shown for dir.
func Pending(dir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dir, FileName))
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	}
	return false, err
}

// Done records that the person has seen the First open screen. Recording it
// again is harmless.
func Done(dir string, now time.Time) (result error) {
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	f, err := localfs.OpenFile(filepath.Join(dir, FileName))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	if err = f.Truncate(0); err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, now.UTC().Format(time.RFC3339))
	return err
}
