package datadir

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
)

// Moved says what Move did.
type Moved struct {
	From, To string
	// Kept is the old folder's new name, or "" when it could not be renamed
	// yet. It then keeps its name and the note, is never used again, and a
	// later Move renames it.
	Kept string
	// Copied is set when this Move copied the data; otherwise it only
	// renamed an old folder an earlier move could not.
	Copied bool
}

// afterCopy, when set by a test, runs between copying and checking the copy.
var afterCopy func(staged string)

// sideLockWait bounds how long Move waits for a command that is changing one
// of the smaller files (agent credentials, webhooks) to finish.
const sideLockWait = 5 * time.Second

// Move moves the data from the old folder to Dir when only the old folder
// holds data. It holds the old folder's locks throughout, so it fails with
// an error matching processlock.ErrBusy while another wasync uses it. It
// copies everything, checks the copy (every file and folder is there with
// its size, nothing changed meanwhile, and each SQLite database passes its
// integrity check) and only then switches. On any failure nothing is
// switched: the copy is removed and the old folder stays in use as it was.
//
// After switching, Move renames the old folder to <name>.moved-<date> with a
// note saying where the data went; where that fails it is retried by the
// next Move. Move returns nil and no error when there is nothing to do.
func (l Location) Move(ctx context.Context, now time.Time) (*Moved, error) {
	if l.Legacy == "" || !HoldsData(l.Legacy) {
		return nil, nil
	}
	if HoldsData(l.Dir) {
		if moved(l.Legacy) {
			return l.finish(now), nil
		}
		// Both hold data, as when an older wasync ran again after a move:
		// Dir is the one in use, and the other one is left alone.
		return nil, nil
	}
	if moved(l.Legacy) {
		return nil, nil
	}
	locks, err := lockFolder(l.Legacy)
	if err != nil {
		if HoldsData(l.Dir) {
			return nil, nil // another wasync moved it meanwhile
		}
		return nil, err
	}
	defer locks.release()
	if HoldsData(l.Dir) || moved(l.Legacy) {
		return nil, nil
	}
	if err = l.copyAndSwitch(ctx); err != nil {
		return nil, err
	}
	return &Moved{From: l.Legacy, To: l.Dir, Kept: retire(l.Legacy, now, &locks), Copied: true}, nil
}

// finish renames an old folder a move switched away from but could not
// rename. Nothing depends on it, so a failure only leaves it for next time.
func (l Location) finish(now time.Time) *Moved {
	locks, err := lockFolder(l.Legacy)
	if err != nil {
		return nil
	}
	defer locks.release()
	if kept := retire(l.Legacy, now, &locks); kept != "" {
		return &Moved{From: l.Legacy, To: l.Dir, Kept: kept}
	}
	return nil
}

func (l Location) copyAndSwitch(ctx context.Context) error {
	staging := filepath.Join(filepath.Dir(l.Dir), "."+filepath.Base(l.Dir)+".moving")
	// What is there is a copy an interrupted move left: never data in use.
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("remove the unfinished copy %s: %w", staging, err)
	}
	if err := localfs.EnsureDir(staging); err != nil {
		return fmt.Errorf("create %s: %w", staging, err)
	}
	switched := false
	defer func() {
		if !switched {
			_ = os.RemoveAll(staging)
		}
	}()
	want, err := scan(l.Legacy)
	if err != nil {
		return err
	}
	if err = copyTree(ctx, l.Legacy, staging, want); err != nil {
		return err
	}
	if afterCopy != nil {
		afterCopy(staging)
	}
	if err = verify(ctx, l.Legacy, staging, want); err != nil {
		return err
	}
	note := filepath.Join(l.Legacy, NoteFile)
	if err = writeNote(note, l.Dir); err != nil {
		return fmt.Errorf("write the note in %s: %w", l.Legacy, err)
	}
	if err = removeEmpty(l.Dir); err == nil {
		err = os.Rename(staging, l.Dir)
	}
	if err != nil {
		_ = os.Remove(note)
		return fmt.Errorf("switch to %s: %w", l.Dir, err)
	}
	switched = true
	_ = localfs.SyncDir(filepath.Dir(l.Dir))
	return nil
}

// entry is one file or folder in a data folder.
type entry struct {
	dir  bool
	size int64
	mod  time.Time
}

// scan lists everything in root but its housekeeping files, by path
// relative to root. It refuses links and other special files: the data
// folder holds none, and wasync never follows one out of it.
func scan(root string) (map[string]entry, error) {
	m := map[string]entry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.Dir(rel) == "." && (housekeeping(rel) || rel == NoteFile) && !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			m[rel] = entry{dir: true}
		case info.Mode().IsRegular():
			m[rel] = entry{size: info.Size(), mod: info.ModTime()}
		default:
			return fmt.Errorf("%s is not a regular file or folder, so wasync does not move it", path)
		}
		return nil
	})
	return m, err
}

func copyTree(ctx context.Context, from, to string, want map[string]entry) error {
	paths := make([]string, 0, len(want))
	for rel := range want {
		paths = append(paths, rel)
	}
	// A folder sorts before everything in it.
	sort.Strings(paths)
	dirs := []string{to}
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, dst := want[rel], filepath.Join(to, rel)
		if e.dir {
			if err := os.Mkdir(dst, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dst)
			continue
		}
		if err := copyFile(ctx, filepath.Join(from, rel), dst, e.mod); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	for _, d := range dirs {
		if err := localfs.SyncDir(d); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies one file into a new private file, written through to the
// disk, with the original's modification time.
func copyFile(ctx context.Context, src, dst string, mod time.Time) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// The folder's private permissions (a private ACL on Windows) carry over.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, ctxReader{ctx, in}); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Chtimes(dst, mod, mod)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// verify checks the copy holds every file and folder with its size, that the
// original did not change while it was copied, and that every SQLite
// database in the copy passes its integrity check.
func verify(ctx context.Context, from, staged string, want map[string]entry) error {
	got, err := scan(staged)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("the copy has %d files and folders, not %d", len(got), len(want))
	}
	for rel, w := range want {
		if g, ok := got[rel]; !ok || g.dir != w.dir || g.size != w.size {
			return fmt.Errorf("the copy of %s is missing or incomplete", rel)
		}
	}
	again, err := scan(from)
	if err != nil {
		return err
	}
	if len(again) != len(want) {
		return errors.New("files changed in the old folder while it was copied")
	}
	for rel, w := range want {
		if a, ok := again[rel]; !ok || a.dir != w.dir || a.size != w.size || !a.mod.Equal(w.mod) {
			return fmt.Errorf("%s changed while it was copied", rel)
		}
	}
	for rel, w := range want {
		if w.dir || filepath.Dir(rel) != "." || !strings.HasSuffix(rel, ".db") {
			continue
		}
		if err = integrity(ctx, filepath.Join(staged, rel)); err != nil {
			return fmt.Errorf("the copy of %s failed its integrity check: %w", rel, err)
		}
	}
	return nil
}

func integrity(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite3", storage.DSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var result string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New(result)
	}
	return nil
}

func writeNote(path, to string) error {
	f, err := localfs.OpenFile(path)
	if err != nil {
		return err
	}
	text := "wasync moved your data to:\n\n  " + to + "\n\n" +
		"This folder is what was here before the move, kept in case you need it.\n" +
		"wasync no longer uses it. Delete it once wasync works from the new folder.\n"
	if err = f.Truncate(0); err == nil {
		_, err = f.WriteString(text)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// removeEmpty removes dir when it holds nothing but housekeeping files, such
// as the lock a command left there, so the copy can take its place.
func removeEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !housekeeping(e.Name()) || !e.Type().IsRegular() {
			return fmt.Errorf("%s is not empty", dir)
		}
		if err = os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}

// retire renames the old folder after the switch and lets go of its locks.
// It returns the new name, or "" when the folder could not be renamed.
func retire(from string, now time.Time, locks *folderLocks) string {
	defer locks.release()
	kept := keptName(from, now)
	if kept == "" {
		return ""
	}
	if os.Rename(from, kept) == nil {
		return kept
	}
	// Windows does not rename a folder while a file in it is open, and the
	// locks are open files.
	locks.release()
	if os.Rename(from, kept) == nil {
		return kept
	}
	return ""
}

func keptName(from string, now time.Time) string {
	base := from + ".moved-" + now.Format("2006-01-02")
	for i := 1; i <= 100; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name
		}
	}
	return ""
}

// folderLocks are the locks of one data folder.
type folderLocks []*processlock.Lock

// lockFolder takes the folder's device lock, which every wasync that uses
// the folder holds, and then every other lock file in it, so no command
// changes a file while it is copied.
func lockFolder(dir string) (folderLocks, error) {
	device, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		return nil, err
	}
	locks := folderLocks{device}
	entries, err := os.ReadDir(dir)
	if err != nil {
		locks.release()
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "device.lock" || !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(name), ".lock") {
			continue
		}
		lock, err := acquireSoon(filepath.Join(dir, name))
		if err != nil {
			locks.release()
			return nil, fmt.Errorf("another wasync command is using %s: %w", name, err)
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func acquireSoon(path string) (*processlock.Lock, error) {
	deadline := time.Now().Add(sideLockWait)
	for {
		lock, err := processlock.Acquire(path)
		if !errors.Is(err, processlock.ErrBusy) || time.Now().After(deadline) {
			return lock, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *folderLocks) release() {
	for _, l := range *f {
		_ = l.Close()
	}
	*f = nil
}
