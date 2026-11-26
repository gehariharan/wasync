package prefs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/localfs"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDefaultsAreChatsWithMediaOn(t *testing.T) {
	s, err := Open(privateDir(t)).Get()
	if err != nil || s.AgentOnly() || !s.MediaOn() || s.Use != "" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	if s, err = (*Store)(nil).Get(); err != nil || !s.MediaOn() {
		t.Fatalf("no store: %+v %v", s, err)
	}
}

func TestUpdateSavesPrivatelyAndKeepsUnknownKeys(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, FileName)
	f, err := localfs.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"first_open_seen":true,"use":"chats"}`)
	f.Close()
	store := Open(dir)
	off := false
	s, err := store.Update(func(s *Settings) { s.Use, s.SaveMedia = UseAgents, &off })
	if err != nil || !s.AgentOnly() || s.MediaOn() {
		t.Fatalf("update: %+v %v", s, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"first_open_seen": true`) || localfs.CheckPrivateFile(path) != nil {
		t.Fatalf("saved file: %s", data)
	}
	// Another store on the same folder sees the change.
	if got, err := Open(dir).Get(); err != nil || !got.AgentOnly() || got.MediaOn() {
		t.Fatalf("second store: %+v %v", got, err)
	}
	// Back to the default removes the key.
	if s, err = store.Update(func(s *Settings) { s.SaveMedia = nil; s.Use = UseChats }); err != nil || !s.MediaOn() || s.AgentOnly() {
		t.Fatalf("reset: %+v %v", s, err)
	}
	if data, _ = os.ReadFile(path); strings.Contains(string(data), "save_media") {
		t.Fatalf("default still written: %s", data)
	}
	if _, err = store.Update(func(s *Settings) { s.Use = "robots" }); err == nil {
		t.Fatal("an unknown use was saved")
	}
}

func TestADamagedFileGivesDefaultsAndIsNotOverwritten(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, FileName)
	for _, bad := range []string{`{"use":`, `{"use":"robots"}`, `{"save_media":"yes"}`} {
		f, err := localfs.OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f.Truncate(0)
		f.WriteString(bad)
		f.Close()
		store := Open(dir)
		if s, err := store.Get(); err == nil || !s.MediaOn() || s.AgentOnly() {
			t.Fatalf("%s: %+v %v", bad, s, err)
		}
		if _, err := store.Update(func(s *Settings) { s.Use = UseAgents }); err == nil {
			t.Fatalf("%s was overwritten", bad)
		}
		if data, _ := os.ReadFile(path); string(data) != bad {
			t.Fatalf("file changed: %s", data)
		}
	}
	if runtime.GOOS != "windows" {
		os.Remove(path)
		if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir).Get(); err == nil {
			t.Fatal("a file others can read was trusted")
		}
	}
}
