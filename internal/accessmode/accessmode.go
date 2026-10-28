// Package accessmode holds the owner's one setup choice about how agents get
// chats: ask the owner each time (the default), or sync all chats and approve
// agents' send, reply and read requests automatically. Only a human changes it
// (interactive TUI or `wasync access mode`); it is a private file next to
// agents.json and has no agent-reachable route.
package accessmode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

const (
	// FileName is the private setting in the data directory.
	FileName = "access-mode.json"
	// Ask is the default: every access request waits for the owner.
	Ask = "ask"
	// All syncs every chat and approves agents' read, send, reply and react
	// requests for a matched chat at once. Edit-own and destructive actions are
	// never automatic.
	All = "all"
	// HistoryNow and HistoryFull are the history depths the owner picks for All.
	HistoryNow  = "now"
	HistoryFull = "full"
)

// Mode is the saved setting.
type Mode struct {
	Mode    string    `json:"mode"`
	History string    `json:"history,omitempty"` // All only: now or full
	SetAt   time.Time `json:"set_at"`
}

// Validate checks a mode before it is saved or applied.
func (m Mode) Validate() error {
	switch {
	case m.Mode == Ask && m.History == "":
		return nil
	case m.Mode == All && (m.History == HistoryNow || m.History == HistoryFull):
		return nil
	case m.Mode == All:
		return fmt.Errorf("sync all needs a history depth: %q or %q", HistoryNow, HistoryFull)
	case m.Mode == Ask:
		return fmt.Errorf("ask each time takes no history depth")
	}
	return fmt.Errorf("mode must be %q or %q", Ask, All)
}

// AutoApproves reports whether agents' read, send, reply and react requests are
// approved without asking.
func (m Mode) AutoApproves() bool { return m.Mode == All }

// Load returns the saved mode, or the default Ask with set=false when the owner
// has not chosen yet. A damaged or non-private file fails closed to an error;
// callers treat that as Ask.
func Load(dir string) (m Mode, set bool, err error) {
	if dir == "" {
		return Mode{Mode: Ask}, false, nil
	}
	path := filepath.Join(dir, FileName)
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return Mode{Mode: Ask}, false, nil
	}
	if err = localfs.CheckPrivateFile(path); err != nil {
		return Mode{Mode: Ask}, false, fmt.Errorf("%s must be a regular private file: %w", FileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return Mode{Mode: Ask}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return Mode{Mode: Ask}, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return Mode{Mode: Ask}, false, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	if err = m.Validate(); err != nil {
		return Mode{Mode: Ask}, false, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	return m, true, nil
}

// Save writes the mode atomically as a private file.
func Save(dir string, m Mode) (result error) {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	m.SetAt = time.Now().UTC().Truncate(time.Second)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+FileName+".tmp")
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, FileName))
}

// Apply makes the owner's choice take effect and then remembers it. Choosing
// All first enables capture for every cached chat with the chosen history
// depth; the setting is saved only when that succeeded, so a failure leaves the
// previous mode in force. Choosing Ask only saves: chats already enabled and
// access already granted stay as they are. It returns how many chats it newly
// enabled.
func Apply(ctx context.Context, store *storage.Store, dir string, account int64, m Mode) (enabled int, err error) {
	if err = m.Validate(); err != nil {
		return 0, err
	}
	if m.Mode == All {
		if enabled, err = store.EnableAllCapture(ctx, account, m.History == HistoryFull); err != nil {
			return 0, err
		}
	}
	if err = Save(dir, m); err != nil {
		return 0, err
	}
	scope := m.Mode
	if m.History != "" {
		scope += "/" + m.History
	}
	// The change is made; a failed audit write is reported but does not undo it.
	if auditErr := agentauth.NewAuditLog(dir).Append(agentauth.AuditEntry{Event: "access_mode", Scope: scope, Outcome: agentauth.OutcomeAllowed}); auditErr != nil {
		return enabled, fmt.Errorf("access mode saved but the audit log is unavailable: %w", auditErr)
	}
	return enabled, nil
}
