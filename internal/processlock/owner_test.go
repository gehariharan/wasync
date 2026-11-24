package processlock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOwnerRecordRoundTripsForALiveProcess(t *testing.T) {
	dir := t.TempDir()
	want := Owner{Kind: "serve", PID: os.Getpid(), DataDir: dir, APIAddr: "127.0.0.1:8787", StartedAt: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), StartedBy: "login item"}
	if err := WriteOwner(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadOwner(dir)
	if !ok || got != want {
		t.Fatalf("read back %+v %v, want %+v", got, ok, want)
	}
	info, err := os.Stat(filepath.Join(dir, OwnerFile))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		t.Fatalf("record is not private: %v %v", info, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %v", entries)
	}
}

func TestOwnerRecordOfAFinishedProcessIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOwner(dir, Owner{Kind: "serve", PID: 2147480000, DataDir: dir, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadOwner(dir); ok {
		t.Fatal("a record for a process that is not running was trusted")
	}
}

func TestOwnerRecordRejectsGarbageAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadOwner(dir); ok {
		t.Fatal("a missing record was trusted")
	}
	path := filepath.Join(dir, OwnerFile)
	for _, content := range []string{"", "not json", `{"kind":"","pid":1}`, `{"kind":"serve","pid":0}`, `{"kind":"serve","pid":-4}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadOwner(dir); ok {
			t.Fatalf("accepted %q", content)
		}
	}
	// A symbolic link is never followed.
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte(`{"kind":"serve","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if err := os.Symlink(other, path); err != nil {
		t.Skip("symbolic links unavailable")
	}
	if _, ok := ReadOwner(dir); ok {
		t.Fatal("followed a symbolic link")
	}
}

func TestRemoveOwnerOnlyRemovesItsOwnRecord(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOwner(dir, Owner{Kind: "serve", PID: os.Getpid(), DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	RemoveOwner(dir, os.Getpid()+1)
	if _, ok := ReadOwner(dir); !ok {
		t.Fatal("removed another process's record")
	}
	RemoveOwner(dir, os.Getpid())
	if _, err := os.Lstat(filepath.Join(dir, OwnerFile)); !os.IsNotExist(err) {
		t.Fatalf("own record not removed: %v", err)
	}
	RemoveOwner(dir, os.Getpid()) // already gone: no error, no panic
}

func TestLongLivedKinds(t *testing.T) {
	for kind, want := range map[string]bool{"serve": true, "tui": true, "run": true, "setup": true, "login": true, "groups": false, "logout": false, "status": false, "": false} {
		if got := (Owner{Kind: kind}).LongLived(); got != want {
			t.Errorf("%q long-lived = %v, want %v", kind, got, want)
		}
	}
}

func TestAliveSeesThisProcessButNotAnImpossibleOne(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process is not alive")
	}
	if Alive(2147480000) {
		t.Fatal("an impossible process id is alive")
	}
}
