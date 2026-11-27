// Package prefs holds the owner's everyday wasync settings: how wasync is used
// (for AI agents only, or also to read chats here) and whether photos, videos
// and files of synced chats are saved. They are comfort settings, not access
// rules: nothing here lets an agent read or send anything.
//
// The settings live in a private file next to the archive. Keys this build does
// not know are kept when it saves, so a newer wasync's settings survive an
// older one changing its own.
package prefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	// FileName is the private settings file in the data directory.
	FileName = "settings.json"
	// UseAgents is "For my AI agents only": wasync keeps chats for agents and
	// runs lighter, and its own chat screens are hidden.
	UseAgents = "agents"
	// UseChats is "Also read my chats here".
	UseChats = "chats"
)

// Settings is the saved choice. The zero value is the default.
type Settings struct {
	// Use is UseAgents or UseChats; "" means the owner has not chosen yet,
	// which behaves as UseChats.
	Use string `json:"use,omitempty"`
	// SaveMedia is the "Also save photos, videos and files" box; nil means
	// the default, which is on.
	SaveMedia *bool `json:"save_media,omitempty"`
	// SetupDone is false while a setup begun on this version is unfinished,
	// and true once the person finished, or skipped, the last setup step.
	// nil means never recorded: an installation from before the setup steps.
	SetupDone *bool `json:"setup_done,omitempty"`
	// SetupStep is the furthest setup step the person reached, so an
	// unfinished setup resumes there.
	SetupStep int `json:"setup_step,omitempty"`
}

// SettingUp reports whether a setup begun on this version is unfinished.
func (s Settings) SettingUp() bool { return s.SetupDone != nil && !*s.SetupDone }

// AgentOnly reports whether wasync is used for AI agents only.
func (s Settings) AgentOnly() bool { return s.Use == UseAgents }

// MediaOn reports whether attachments of synced chats are saved by default.
func (s Settings) MediaOn() bool { return s.SaveMedia == nil || *s.SaveMedia }

// Validate checks a choice before it is saved.
func (s Settings) Validate() error {
	switch s.Use {
	case "", UseAgents, UseChats:
		return nil
	}
	return fmt.Errorf("use must be %q or %q", UseAgents, UseChats)
}

// Store reads and writes the settings of one data directory. Reads are cached
// and checked against the file, so two stores on the same directory, or an edit
// by another wasync command, never disagree for long.
type Store struct {
	dir string
	mu  sync.Mutex
	// stamp identifies the file version cached; zero when nothing is cached.
	stamp  fileStamp
	cached Settings
}

type fileStamp struct {
	exists bool
	size   int64
	mod    time.Time
}

// Open returns the settings store of a data directory. It reads nothing yet.
func Open(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path() string { return filepath.Join(s.dir, FileName) }

func (s *Store) stampNow() (fileStamp, error) {
	info, err := os.Lstat(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{exists: true, size: info.Size(), mod: info.ModTime()}, nil
}

// Get returns the current settings. A missing file is the defaults. A damaged
// or non-private file also gives the defaults, together with the error, so
// callers can carry on and say why their choice was not applied.
func (s *Store) Get() (Settings, error) {
	if s == nil || s.dir == "" {
		return Settings{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked()
}

func (s *Store) getLocked() (Settings, error) {
	stamp, err := s.stampNow()
	if err != nil {
		return Settings{}, err
	}
	if !stamp.exists {
		s.stamp, s.cached = fileStamp{}, Settings{}
		return Settings{}, nil
	}
	if stamp == s.stamp {
		return s.cached, nil
	}
	raw, err := s.readRaw()
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	if err = decode(raw, &out); err != nil {
		return Settings{}, err
	}
	s.stamp, s.cached = stamp, out
	return out, nil
}

// readRaw reads the file as its separate keys, so saving keeps unknown ones.
func (s *Store) readRaw() (map[string]json.RawMessage, error) {
	path := s.path()
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return nil, fmt.Errorf("%s must be a regular private file: %w", FileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return nil, err
	}
	raw := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err = json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", FileName, err)
		}
	}
	return raw, nil
}

func decode(raw map[string]json.RawMessage, out *Settings) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid %s: %w", FileName, err)
	}
	if err = out.Validate(); err != nil {
		return fmt.Errorf("invalid %s: %w", FileName, err)
	}
	return nil
}

// Update changes the settings with change and saves them atomically as a
// private file. A damaged file is not overwritten: the error is returned and
// nothing changes.
func (s *Store) Update(change func(*Settings)) (Settings, error) {
	if s == nil || s.dir == "" {
		return Settings{}, fmt.Errorf("no data directory for settings")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.readRaw()
	if err != nil {
		return Settings{}, err
	}
	var current Settings
	if err = decode(raw, &current); err != nil {
		return Settings{}, err
	}
	next := current
	if current.SaveMedia != nil {
		v := *current.SaveMedia
		next.SaveMedia = &v
	}
	if current.SetupDone != nil {
		v := *current.SetupDone
		next.SetupDone = &v
	}
	change(&next)
	if err = next.Validate(); err != nil {
		return current, err
	}
	setKey(raw, "use", next.Use, next.Use == "")
	if next.SaveMedia == nil {
		delete(raw, "save_media")
	} else {
		setKey(raw, "save_media", *next.SaveMedia, false)
	}
	if next.SetupDone == nil {
		delete(raw, "setup_done")
	} else {
		setKey(raw, "setup_done", *next.SetupDone, false)
	}
	setKey(raw, "setup_step", next.SetupStep, next.SetupStep == 0)
	if err = s.write(raw); err != nil {
		return current, err
	}
	s.stamp = fileStamp{}
	return s.getLocked()
}

func setKey(raw map[string]json.RawMessage, key string, value any, remove bool) {
	if remove {
		delete(raw, key)
		return
	}
	data, _ := json.Marshal(value)
	raw[key] = data
}

func (s *Store) write(raw map[string]json.RawMessage) (result error) {
	if err := localfs.EnsureDir(s.dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "."+FileName+".tmp")
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
	if err = os.Rename(tmp, s.path()); err != nil {
		return err
	}
	return localfs.SyncDir(s.dir)
}
