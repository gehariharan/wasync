// Package opensend holds the owner's "open sending" switch: for a limited time,
// any agent that already holds the send scope may message any chat or phone
// number, not just the chats the owner approved one by one, up to a small cap
// per hour. It is a private file next to agents.json. Only a person at the
// computer switches it on (the web page asks for the system confirmation
// first); anyone may switch it off, and it switches itself off when the time is
// up. A missing, damaged or expired file means off.
package opensend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	// FileName is the private setting in the data directory.
	FileName = "open-sending.json"
	// Duration is how long one switch-on lasts.
	Duration = time.Hour
	// Window is the period the two caps below are counted over.
	Window = time.Hour
	// MaxMessages is how many messages open sending allows per Window, over
	// all agents together.
	MaxMessages = 15
	// MaxNewNumbers is how many chats the archive has never seen that open
	// sending may start per Window.
	MaxNewNumbers = 5
)

// State is the saved switch.
type State struct {
	StartedAt time.Time `json:"started_at"`
	Until     time.Time `json:"until"`
}

// Active reports whether the switch is on at now.
func (s State) Active(now time.Time) bool { return now.Before(s.Until) && !s.Until.IsZero() }

// Effect is the plain-words effect of switching it on, for the prompt that asks
// the person to confirm.
func Effect() string {
	return fmt.Sprintf("let any agent that can send message ANY chat or phone number for the next hour, up to %d messages and %d new numbers an hour, without asking you each time", MaxMessages, MaxNewNumbers)
}

// Load returns the saved switch. A missing file is off; a damaged or
// non-private file is off with an error the caller may show.
func Load(dir string) (State, error) {
	if dir == "" {
		return State{}, nil
	}
	path := filepath.Join(dir, FileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return State{}, fmt.Errorf("%s must be a regular private file: %w", FileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return State{}, err
	}
	var s State
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&s); err != nil {
		return State{}, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	return s, nil
}

// ActiveNow reports whether open sending is on right now, treating any problem
// reading the file as off.
func ActiveNow(dir string, now time.Time) (State, bool) {
	s, err := Load(dir)
	if err != nil || !s.Active(now) {
		return State{}, false
	}
	return s, true
}

// Start switches open sending on for Duration from now.
func Start(dir string, now time.Time) (State, error) {
	s := State{StartedAt: now.UTC().Truncate(time.Second), Until: now.UTC().Truncate(time.Second).Add(Duration)}
	return s, Save(dir, s)
}

// Stop switches it off. Switching off twice is harmless.
func Stop(dir string) error {
	if err := os.Remove(filepath.Join(dir, FileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Save writes the switch atomically as a private file.
func Save(dir string, s State) (result error) {
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
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
