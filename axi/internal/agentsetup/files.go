// Package agentsetup installs and inspects wasync-axi's opt-in agent
// integrations: a session-start hook in each agent's settings (AXI
// principle 7, ambient context) and the wasync-axi agent skill. It edits
// only what it owns, preserves every other byte of the user's files, writes
// atomically with private permissions and refuses files it cannot parse.
// It never touches wasync, its data directory or its tokens.
package agentsetup

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

// maxFileSize bounds the agent settings files wasync-axi reads.
const maxFileSize = 4 << 20

// Error codes of setup problems.
const (
	CodeConfigInvalid = "config_invalid" // a file exists but cannot be edited safely
	CodeWriteFailed   = "write_failed"   // reading or writing a file failed
)

// file is the current state of a file wasync-axi may change.
type file struct {
	Path   string // as given
	Target string // Path with symlinks resolved; the file that is written
	Exists bool
	Data   []byte
}

// readFile reads path, following symlinks so a dotfiles-managed settings
// file is edited in place instead of being replaced by a regular file.
func readFile(path string) (file, error) {
	f := file{Path: path, Target: path}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, ioErr(path, "cannot read", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if errors.Is(err, fs.ErrNotExist) {
			return f, &contract.Error{Code: CodeConfigInvalid, Message: path + " is a symlink to a missing file",
				Help: "fix or remove the symlink, then rerun the command"}
		}
		if err != nil {
			return f, ioErr(path, "cannot resolve", err)
		}
		f.Target = target
		if info, err = os.Stat(target); err != nil {
			return f, ioErr(path, "cannot read", err)
		}
	}
	if !info.Mode().IsRegular() {
		return f, &contract.Error{Code: CodeConfigInvalid, Message: path + " is not a regular file", Help: "move it away, then rerun the command"}
	}
	h, err := os.Open(f.Target)
	if err != nil {
		return f, ioErr(path, "cannot read", err)
	}
	defer h.Close()
	data, err := io.ReadAll(io.LimitReader(h, maxFileSize+1))
	if err != nil {
		return f, ioErr(path, "cannot read", err)
	}
	if len(data) > maxFileSize {
		return f, &contract.Error{Code: CodeConfigInvalid, Message: path + " is larger than 4 MiB", Help: "wasync-axi does not edit files this large; add the hook by hand"}
	}
	f.Exists, f.Data = true, data
	return f, nil
}

// writeAtomic replaces target with data through a temporary file in the
// same directory and a rename, so readers never see a partial file. The
// result has mode 0600; missing parent directories are created with 0700.
func writeAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ioErr(dir, "cannot create", err)
	}
	tmp, err := os.CreateTemp(dir, ".wasync-axi-*.tmp")
	if err != nil {
		return ioErr(target, "cannot write", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return ioErr(target, "cannot write", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return ioErr(target, "cannot write", err)
	}
	if err := tmp.Sync(); err != nil {
		return ioErr(target, "cannot write", err)
	}
	if err := tmp.Close(); err != nil {
		return ioErr(target, "cannot write", err)
	}
	if err := os.Rename(name, target); err != nil {
		return ioErr(target, "cannot replace", err)
	}
	ok = true
	return nil
}

func ioErr(path, what string, err error) error {
	msg := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		msg = pe.Err.Error()
	}
	return &contract.Error{Code: CodeWriteFailed, Message: what + " " + path + ": " + msg, Help: "check the file's owner and permissions, then rerun the command"}
}
