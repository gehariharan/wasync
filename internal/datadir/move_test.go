package datadir

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

var day = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

// oldFolder makes a data folder the way an earlier version leaves it: a
// real archive and session database, media in subfolders, the smaller
// files, and the lock and owner record of the last run.
func oldFolder(t *testing.T) Location {
	t.Helper()
	base := t.TempDir()
	l := Location{Dir: filepath.Join(base, "share", "wasync"), Legacy: filepath.Join(base, "config", "wasync")}
	// Private as wasync makes it, so a Windows ACL check passes too.
	if err := localfs.EnsureDir(l.Legacy); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), filepath.Join(l.Legacy, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", storage.DSN(filepath.Join(l.Legacy, "session.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE device (jid TEXT); INSERT INTO device VALUES ('123@s.whatsapp.net')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(l.Legacy, "media", "2026", "11", "photo.jpg"), strings.Repeat("jpeg", 4096))
	write(t, filepath.Join(l.Legacy, "agents.json"), `{"clients":[]}`)
	write(t, filepath.Join(l.Legacy, "api.token"), "secret")
	write(t, filepath.Join(l.Legacy, "agents.lock"), "")
	write(t, filepath.Join(l.Legacy, "device.lock"), "")
	write(t, filepath.Join(l.Legacy, processlock.OwnerFile), `{"kind":"serve","pid":1}`)
	if err = os.MkdirAll(filepath.Join(l.Legacy, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err = os.Chtimes(filepath.Join(l.Legacy, "media", "2026", "11", "photo.jpg"), old, old); err != nil {
		t.Fatal(err)
	}
	return l
}

// untouched checks a failed move changed nothing the person relies on.
func untouched(t *testing.T, l Location, before map[string]entry) {
	t.Helper()
	if l.InUse() != l.Legacy {
		t.Fatal("the old folder must stay in use after a failed move")
	}
	if _, err := os.Lstat(l.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed move left the new folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(l.Dir), ".wasync.moving")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed move left its copy: %v", err)
	}
	if moved(l.Legacy) {
		t.Fatal("a failed move left the note")
	}
	after, err := scan(l.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the old folder changed: %d entries, was %d", len(after), len(before))
	}
	for rel, b := range before {
		if a := after[rel]; a.dir != b.dir || a.size != b.size || !a.mod.Equal(b.mod) {
			t.Fatalf("%s changed in the old folder", rel)
		}
	}
}

func TestMoveCopiesChecksAndSwitches(t *testing.T) {
	l := oldFolder(t)
	before, err := scan(l.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	if l.InUse() != l.Legacy {
		t.Fatal("before the move the old folder is in use")
	}
	m, err := l.Move(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	kept := l.Legacy + ".moved-2026-11-26"
	if m == nil || !m.Copied || m.From != l.Legacy || m.To != l.Dir || m.Kept != kept {
		t.Fatalf("moved %+v", m)
	}
	if l.InUse() != l.Dir {
		t.Fatal("after the move the new folder is in use")
	}
	if _, err = os.Lstat(l.Legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old folder keeps its name")
	}
	note, err := os.ReadFile(filepath.Join(kept, NoteFile))
	if err != nil || !strings.Contains(string(note), l.Dir) {
		t.Fatalf("the kept folder must say where the data went: %q %v", note, err)
	}
	if KeptCopy(l.Dir) != kept {
		t.Fatalf("the new folder must say where the old one is kept: %q", KeptCopy(l.Dir))
	}
	got, err := scan(l.Dir)
	if err != nil {
		t.Fatal(err)
	}
	delete(got, KeptNoteFile)
	if len(got) != len(before) {
		t.Fatalf("new folder has %d entries, want %d", len(got), len(before))
	}
	for rel, b := range before {
		g := got[rel]
		if g.dir != b.dir || (!b.dir && g.size != b.size && !strings.HasSuffix(rel, ".db")) {
			t.Fatalf("%s did not arrive whole", rel)
		}
	}
	photo := filepath.Join(l.Dir, "media", "2026", "11", "photo.jpg")
	if info, err := os.Stat(photo); err != nil || !info.ModTime().Equal(before[filepath.Join("media", "2026", "11", "photo.jpg")].mod) {
		t.Fatalf("the photo must keep its time: %v", err)
	}
	for _, name := range []string{"device.lock", processlock.OwnerFile, NoteFile} {
		if _, err := os.Lstat(filepath.Join(l.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not be copied", name)
		}
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{l.Dir, filepath.Join(l.Dir, "media"), filepath.Join(l.Dir, "archive.db"), photo} {
			info, err := os.Stat(p)
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s is not private: %v %v", p, info.Mode(), err)
			}
		}
	}
	store, err := storage.Open(context.Background(), filepath.Join(l.Dir, "archive.db"))
	if err != nil {
		t.Fatalf("the moved archive must open: %v", err)
	}
	store.Close()

	again, err := l.Move(context.Background(), day)
	if err != nil || again != nil {
		t.Fatalf("a second move must do nothing: %+v %v", again, err)
	}
}

func TestMoveIntoAFolderHoldingOnlyALock(t *testing.T) {
	l := oldFolder(t)
	write(t, filepath.Join(l.Dir, "device.lock"), "")
	if m, err := l.Move(context.Background(), day); err != nil || m == nil || !m.Copied {
		t.Fatalf("%+v %v", m, err)
	}
	if !HoldsData(l.Dir) || l.InUse() != l.Dir {
		t.Fatal("the data must be in the new folder")
	}
}

func TestFailedMoveKeepsTheOldFolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(t *testing.T, l Location, staged string)
		want  string
	}{
		{"a damaged database", func(t *testing.T, _ Location, staged string) {
			f, err := os.OpenFile(filepath.Join(staged, "archive.db"), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err = f.WriteAt([]byte(strings.Repeat("\xff", 512)), 4096); err != nil {
				t.Fatal(err)
			}
		}, "integrity check"},
		{"a missing file", func(t *testing.T, _ Location, staged string) {
			if err := os.Remove(filepath.Join(staged, "media", "2026", "11", "photo.jpg")); err != nil {
				t.Fatal(err)
			}
		}, "files and folders"},
		{"a short file", func(t *testing.T, _ Location, staged string) {
			if err := os.Truncate(filepath.Join(staged, "media", "2026", "11", "photo.jpg"), 10); err != nil {
				t.Fatal(err)
			}
		}, "incomplete"},
		{"a change while copying", func(t *testing.T, l Location, _ string) {
			write(t, filepath.Join(l.Legacy, "agents.json"), `{"clients":[{"name":"late"}]}`)
		}, "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := oldFolder(t)
			afterCopy = func(staged string) { tc.after(t, l, staged) }
			defer func() { afterCopy = nil }()
			before, err := scan(l.Legacy)
			if err != nil {
				t.Fatal(err)
			}
			m, err := l.Move(context.Background(), day)
			if err == nil || m != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error about %q, got %+v %v", tc.want, m, err)
			}
			if tc.name == "a change while copying" {
				before, _ = scan(l.Legacy)
			}
			untouched(t, l, before)
			afterCopy = nil
			if m, err = l.Move(context.Background(), day); err != nil || m == nil || !m.Copied {
				t.Fatalf("the next start must move it: %+v %v", m, err)
			}
		})
	}
}

func TestInterruptedMoveKeepsTheOldFolder(t *testing.T) {
	l := oldFolder(t)
	before, err := scan(l.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m, err := l.Move(ctx, day); !errors.Is(err, context.Canceled) || m != nil {
		t.Fatalf("%+v %v", m, err)
	}
	untouched(t, l, before)
	// A copy a crash left behind is replaced, never used.
	write(t, filepath.Join(filepath.Dir(l.Dir), ".wasync.moving", "archive.db"), "half")
	if m, err := l.Move(context.Background(), day); err != nil || m == nil || !m.Copied {
		t.Fatalf("%+v %v", m, err)
	}
	if info, err := os.Stat(filepath.Join(l.Dir, "archive.db")); err != nil || info.Size() == 4 {
		t.Fatal("the leftover copy was used")
	}
}

func TestUnreadableFileKeepsTheOldFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions that bind the test user")
	}
	l := oldFolder(t)
	secret := filepath.Join(l.Legacy, "api.token")
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(secret, 0o600)
	before, err := scan(l.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := l.Move(context.Background(), day); err == nil || m != nil {
		t.Fatalf("%+v %v", m, err)
	}
	untouched(t, l, before)
}

func TestMoveRefusesWhileAnotherWasyncUsesTheOldFolder(t *testing.T) {
	l := oldFolder(t)
	before, err := scan(l.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := processlock.Acquire(filepath.Join(l.Legacy, "device.lock"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := l.Move(context.Background(), day)
	if !errors.Is(err, processlock.ErrBusy) || m != nil {
		t.Fatalf("%+v %v", m, err)
	}
	untouched(t, l, before)
	lock.Close()
	if m, err = l.Move(context.Background(), day); err != nil || m == nil || !m.Copied {
		t.Fatalf("once it stops the data moves: %+v %v", m, err)
	}
}

func TestMoveWaitsForACommandChangingASmallerFile(t *testing.T) {
	l := oldFolder(t)
	lock, err := processlock.Acquire(filepath.Join(l.Legacy, "agents.lock"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(200 * time.Millisecond); lock.Close() }()
	if m, err := l.Move(context.Background(), day); err != nil || m == nil || !m.Copied {
		t.Fatalf("a command that finishes soon is waited for: %+v %v", m, err)
	}
}

func TestConcurrentStartsMoveOnce(t *testing.T) {
	l := oldFolder(t)
	var wg sync.WaitGroup
	results := make([]*Moved, 6)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = l.Move(context.Background(), day)
		}(i)
	}
	wg.Wait()
	copied := 0
	for i, m := range results {
		if errs[i] != nil && !errors.Is(errs[i], processlock.ErrBusy) {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if m != nil && m.Copied {
			copied++
		}
	}
	if copied != 1 || l.InUse() != l.Dir || !HoldsData(l.Dir) {
		t.Fatalf("%d starts copied the data", copied)
	}
}

func TestMoveLeavesTwoFoldersWithDataAlone(t *testing.T) {
	l := oldFolder(t)
	write(t, filepath.Join(l.Dir, "archive.db"), "")
	if m, err := l.Move(context.Background(), day); err != nil || m != nil {
		t.Fatalf("%+v %v", m, err)
	}
	if l.InUse() != l.Dir || moved(l.Legacy) {
		t.Fatal("the new folder is in use and the old one is left as it is")
	}
}

func TestMoveRenamesAnOldFolderAnEarlierMoveCouldNot(t *testing.T) {
	l := oldFolder(t)
	write(t, filepath.Join(l.Dir, "archive.db"), "")
	write(t, filepath.Join(l.Legacy, NoteFile), "moved")
	if l.InUse() != l.Dir {
		t.Fatal("a folder with the note is never used again")
	}
	write(t, l.Legacy+".moved-2026-11-26", "taken")
	m, err := l.Move(context.Background(), day)
	if err != nil || m == nil || m.Copied || m.Kept != l.Legacy+".moved-2026-11-26-2" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err = os.Lstat(l.Legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old folder keeps its name")
	}
}

func TestMoveKeepsTheOldFolderInKeepIn(t *testing.T) {
	l := oldFolder(t)
	l.KeepIn = filepath.Join(filepath.Dir(filepath.Dir(l.Dir)), "local")
	if err := os.Mkdir(l.KeepIn, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := l.Move(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(l.KeepIn, "wasync.moved-2026-11-26")
	if m == nil || m.Kept != kept {
		t.Fatalf("the old folder must leave the roaming folder: %+v", m)
	}
	if _, err = os.Stat(filepath.Join(kept, "archive.db")); err != nil {
		t.Fatalf("the kept folder must hold the old data: %v", err)
	}
	if _, err = os.Lstat(l.Legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nothing may stay in the old place")
	}
	if got := KeptCopy(l.Dir); got != kept {
		t.Fatalf("the data folder's note must name the kept folder: %q", got)
	}
}

func TestMoveKeepsTheOldFolderBesideWhenKeepInFails(t *testing.T) {
	l := oldFolder(t)
	// A folder that does not exist takes no rename.
	l.KeepIn = filepath.Join(t.TempDir(), "missing")
	m, err := l.Move(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	kept := l.Legacy + ".moved-2026-11-26"
	if m == nil || m.Kept != kept {
		t.Fatalf("the old folder must be kept beside where it was: %+v", m)
	}
	if _, err = os.Stat(filepath.Join(kept, NoteFile)); err != nil {
		t.Fatalf("the kept folder must keep its note: %v", err)
	}
	if got := KeptCopy(l.Dir); got != kept {
		t.Fatalf("the data folder's note must name the kept folder: %q", got)
	}
}

func TestNewFolderIsNotInUseUntilItHoldsTheData(t *testing.T) {
	l := oldFolder(t)
	var during string
	beforeSwitch = func() { during = l.InUse() }
	defer func() { beforeSwitch = nil }()
	if _, err := l.Move(context.Background(), day); err != nil {
		t.Fatal(err)
	}
	if during != l.Legacy {
		t.Fatalf("while the new folder is still empty the old one is in use, not %s", during)
	}
	if l.InUse() != l.Dir || !HoldsData(l.Dir) {
		t.Fatal("after the move the new folder holds the data and is in use")
	}
}

func TestNothingToMove(t *testing.T) {
	base := t.TempDir()
	for _, l := range []Location{
		{Dir: filepath.Join(base, "mac")},
		{Dir: filepath.Join(base, "new"), Legacy: filepath.Join(base, "absent")},
	} {
		if m, err := l.Move(context.Background(), day); err != nil || m != nil {
			t.Fatalf("%+v: %+v %v", l, m, err)
		}
	}
	l := Location{Dir: filepath.Join(base, "new2"), Legacy: filepath.Join(base, "locks-only")}
	write(t, filepath.Join(l.Legacy, "device.lock"), "")
	if m, err := l.Move(context.Background(), day); err != nil || m != nil {
		t.Fatalf("a folder with only a lock has nothing to move: %+v %v", m, err)
	}
}
