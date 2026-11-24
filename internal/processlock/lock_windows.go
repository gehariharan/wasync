//go:build windows

package processlock

import (
	"errors"
	"fmt"
	"os"

	"github.com/gehariharan/wasync/internal/localfs"
	"golang.org/x/sys/windows"
)

var ErrBusy = errors.New("another wasync is using this data directory")

type Lock struct{ file *os.File }

func Acquire(path string) (*Lock, error) {
	f, err := localfs.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open process lock: %w", err)
	}
	var overlap windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock device store: %w", err)
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error {
	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return f.Close()
}
