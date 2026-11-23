package processlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// OwnerFile is the record, kept beside device.lock, of which process holds it.
const OwnerFile = "owner.json"

// Owner says who holds the data directory. The lock itself is the truth about
// whether anyone does; this record only says who, so a refused command can
// name the process and a later command can reach it.
type Owner struct {
	// Kind is the command that took the directory: serve, tui, run, setup, login
	// or a short command such as groups.
	Kind    string `json:"kind"`
	PID     int    `json:"pid"`
	DataDir string `json:"data_dir"`
	// APIAddr is where the owner's local API listens; empty while it has none.
	APIAddr   string    `json:"api_addr,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// StartedBy is "login item" or "manually" when known, and empty otherwise.
	StartedBy string `json:"started_by,omitempty"`
}

// LongLived reports whether the owner keeps running until someone stops it,
// as opposed to a command that finishes by itself in moments.
func (o Owner) LongLived() bool {
	switch o.Kind {
	case "serve", "tui", "run", "setup", "login":
		return true
	}
	return false
}

// WriteOwner replaces the record with o. It is written whole and renamed into
// place, so a reader never sees half of it.
func WriteOwner(dir string, o Owner) error {
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".owner-*")
	if err != nil {
		return fmt.Errorf("write owner record: %w", err)
	}
	defer os.Remove(tmp.Name()) // a no-op after a successful rename
	if _, err = tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write owner record: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("write owner record: %w", err)
	}
	if err = os.Rename(tmp.Name(), filepath.Join(dir, OwnerFile)); err != nil {
		return fmt.Errorf("write owner record: %w", err)
	}
	return nil
}

// ReadOwner returns the record only when it names a process that is still
// running. A record left behind by a process that crashed is ignored. Callers
// must also check Held: a live process id alone does not prove it holds the lock.
func ReadOwner(dir string) (Owner, bool) {
	path := filepath.Join(dir, OwnerFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Owner{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Owner{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return Owner{}, false
	}
	var o Owner
	if err = json.Unmarshal(data, &o); err != nil || o.PID <= 0 || o.Kind == "" || !Alive(o.PID) {
		return Owner{}, false
	}
	return o, true
}

// RemoveOwner deletes the record when it still belongs to pid.
func RemoveOwner(dir string, pid int) {
	path := filepath.Join(dir, OwnerFile)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return
	}
	if o, ok := readRaw(path); ok && o.PID == pid {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return
		}
	}
}

func readRaw(path string) (Owner, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Owner{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	var o Owner
	if err != nil || json.Unmarshal(data, &o) != nil {
		return Owner{}, false
	}
	return o, true
}
