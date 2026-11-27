package agentconnect

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// maxFileSize bounds the settings files wasync reads. Claude Code's
// ~/.claude.json also keeps per-project state, so it can grow large.
const maxFileSize = 32 << 20

// ErrChanged means the file changed after its preview was made.
var ErrChanged = errors.New("the settings file changed since the preview; nothing was written")

// file is the current state of a settings file.
type file struct {
	Path   string // as given
	Target string // Path with symlinks resolved; the file that is written
	Exists bool
	Mode   fs.FileMode
	Data   []byte
}

// readFile reads path, following a symlink so a dotfiles-managed file is
// edited in place instead of being replaced by a regular file.
func readFile(path string) (file, error) {
	f := file{Path: path, Target: path, Mode: 0o600}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return f, fmt.Errorf("%s is a symlink wasync cannot follow: %w", path, err)
		}
		f.Target = target
		if info, err = os.Stat(target); err != nil {
			return f, err
		}
	}
	if !info.Mode().IsRegular() {
		return f, fmt.Errorf("%s is not a regular file", path)
	}
	h, err := os.Open(f.Target)
	if err != nil {
		return f, err
	}
	defer h.Close()
	data, err := io.ReadAll(io.LimitReader(h, maxFileSize+1))
	if err != nil {
		return f, err
	}
	if len(data) > maxFileSize {
		return f, fmt.Errorf("%s is larger than %d MiB", path, maxFileSize>>20)
	}
	f.Exists, f.Mode, f.Data = true, info.Mode().Perm(), data
	return f, nil
}

// backup copies the file's current bytes next to it, named with the time,
// and returns the copy's path; "" when there is no file yet.
func backup(f file, stamp string) (string, error) {
	if !f.Exists {
		return "", nil
	}
	base := f.Target + ".wasync-backup-" + stamp
	name := base
	for i := 2; ; i++ {
		h, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) && i < 100 {
			name = base + "-" + strconv.Itoa(i)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("back up %s: %w", f.Path, err)
		}
		_, err = h.Write(f.Data)
		if err == nil {
			err = h.Sync()
		}
		if cerr := h.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("back up %s: %w", f.Path, err)
		}
		return name, nil
	}
}

// writeAtomic replaces target with data through a temporary file in the
// same folder and a rename, so the app never reads a half-written file. The
// file keeps its permissions; a new one is private (0600) in a folder
// created private (0700).
func writeAtomic(target string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wasync-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(mode); err != nil && !isWindows() {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	ok = true
	return nil
}
