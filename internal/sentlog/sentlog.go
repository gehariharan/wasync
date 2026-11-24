// Package sentlog keeps a private record of what was sent from this computer,
// by the owner or by an agent. It stores who sent, to which chat, when, and what
// WhatsApp said about it, but never the message text: the text is already in the
// archive as an ordinary message from the owner, and the log points at it by its
// WhatsApp message ID. That keeps message bodies out of a second file.
package sentlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	// FileName is the private JSON-lines log in the data directory.
	FileName = "sent-log.jsonl"
	// MaxBytes triggers rotation; one older file (.1) is kept.
	MaxBytes = 2 << 20

	maxFieldLen = 256
)

// Outcomes. "unknown" means WhatsApp may or may not have delivered it: the
// request left but no acknowledgement came back, so it must not be retried
// automatically.
const (
	OutcomeSent    = "sent"
	OutcomeFailed  = "failed"
	OutcomeUnknown = "unknown"
	OutcomeBlocked = "blocked"
)

// Actors. An agent is recorded under its own name.
const Owner = "owner"

// Entry is one attempt to send.
type Entry struct {
	Time      time.Time `json:"time"`
	Actor     string    `json:"actor"` // Owner, or the agent's name
	Kind      string    `json:"kind"`  // "send" or "reply"
	Chat      string    `json:"chat"`
	MessageID string    `json:"message_id,omitempty"`
	Outcome   string    `json:"outcome"`
	Code      string    `json:"error_code,omitempty"`
	// Open marks a send allowed by open sending rather than by a chat the
	// owner approved; NewNumber marks one that started a chat the archive
	// had never seen. Open sending counts both against its hourly caps.
	Open      bool `json:"open,omitempty"`
	NewNumber bool `json:"new_number,omitempty"`
}

// Log appends entries to the sent log. Only the running owner writes it, so a
// process-wide mutex is enough.
type Log struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

// New returns the sent log kept in dir.
func New(dir string) *Log { return &Log{dir: dir, maxBytes: MaxBytes} }

func (l *Log) path() string { return filepath.Join(l.dir, FileName) }

func clip(s string) string {
	if len(s) > maxFieldLen {
		return s[:maxFieldLen]
	}
	return s
}

// Append writes one entry. A missing time means now. A nil log discards it.
func (l *Log) Append(e Entry) (result error) {
	if l == nil {
		return nil
	}
	if e.Actor == "" || e.Kind == "" || e.Chat == "" || e.Outcome == "" {
		return fmt.Errorf("sent log entry needs an actor, a kind, a chat and an outcome")
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC().Truncate(time.Second)
	e.Actor, e.Kind, e.Chat, e.MessageID, e.Outcome, e.Code = clip(e.Actor), clip(e.Kind), clip(e.Chat), clip(e.MessageID), clip(e.Outcome), clip(e.Code)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if info, statErr := os.Lstat(l.path()); statErr == nil && info.Mode().IsRegular() && info.Size()+int64(len(line)) > l.maxBytes {
		if err = os.Rename(l.path(), l.path()+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// localfs.OpenFile creates the file privately and refuses symlinks; it does
	// not append, so seek to the end.
	f, err := localfs.OpenFile(l.path())
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	if _, err = f.Seek(0, 2); err != nil {
		return err
	}
	if _, err = f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// Recent returns up to limit entries, newest first, from the current and the
// previous file. A line that does not parse is skipped rather than hiding the rest.
func (l *Log) Recent(limit int) ([]Entry, error) {
	if l == nil || limit <= 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []Entry
	for _, path := range []string{l.path() + ".1", l.path()} {
		entries, err := readFile(path)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	out := make([]Entry, 0, min(limit, len(all)))
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out, nil
}

func readFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Actor != "" {
			out = append(out, e)
		}
	}
	return out, scanner.Err()
}
