//go:build darwin || linux

// Package processlock prevents simultaneous ownership of one device store.
package processlock

import (
	"errors"
	"fmt"
	"github.com/gehariharan/wasync/internal/localfs"
	"golang.org/x/sys/unix"
	"os"
)

var ErrBusy = errors.New("another wasync is using this data directory")

type Lock struct{ file *os.File }

func Acquire(path string) (*Lock, error) {
	f, err := localfs.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open process lock: %w", err)
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock device store: %w", err)
	}
	return &Lock{file: f}, nil
}

// Keep the lock file: unlinking it permits different processes to lock different inodes.
func (l *Lock) Close() error {
	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return f.Close() // Closing releases the advisory lock, including on process exit.
}
