// Package uninstall removes wasync from a computer without a terminal. The
// running wasync records what to do and starts a short-lived helper, then
// stops; the helper waits for it and for the wasync app to quit, removes the
// start-at-login items, deletes the data when the person asked for that, and
// hands over to the platform's own way of removing the program files: the
// Windows Setup uninstaller, or moving wasync.app to the Bin on macOS.
//
// The request lives in a private folder in the system's temporary folder,
// not in the data directory, because the helper may delete the data
// directory. It is modelled on internal/update, which runs a helper the same way.
package uninstall

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// The kinds of hand-off. Only KindMacApp and KindWindowsSetup remove the
// program files by themselves; KindManual says how in Note.
const (
	KindMacApp       = "mac-app"
	KindWindowsSetup = "windows-setup"
	KindManual       = "manual"
)

// Handoff is how wasync's program files are removed once it has stopped.
type Handoff struct {
	Kind string `json:"kind"`
	// Target is the wasync.app bundle, or the Setup uninstaller (unins000.exe).
	Target string `json:"target,omitempty"`
	// Note is, for KindManual, one or two plain sentences on removing the
	// program files.
	Note string `json:"note,omitempty"`
}

// Request is what the helper does once wasync has stopped.
type Request struct {
	// DataDir is the absolute data directory of the wasync being removed.
	DataDir string `json:"data_dir"`
	// DeleteData deletes the data directory and the keys wasync keeps for it
	// elsewhere. The caller has already had the person confirm it.
	DeleteData bool `json:"delete_data"`
	// AppPID is the wasync app process to wait for, or 0 when there is none.
	AppPID  int     `json:"app_pid,omitempty"`
	Handoff Handoff `json:"handoff"`
	// Created is when Start wrote the request.
	Created time.Time `json:"created"`
}

// ApplyCommand is the hidden wasync command that runs the helper:
// `wasync remove-apply REQUEST_FILE`.
const ApplyCommand = "remove-apply"

const (
	// markerFile, in the data directory, says wasync is stopping to be removed.
	markerFile = "remove.pending"
	// requestFile and logFile sit in the helper's private temporary folder.
	requestFile = "request.json"
	logFile     = "remove.log"
	// helperOut collects anything the helper prints outside its log.
	helperOut = "helper-output.log"
	// tempPrefix names the helper's temporary folder.
	tempPrefix = "wasync-remove-"
)

// MarkerPath is the note, in the data directory, that wasync is stopping to
// be removed; Pending reads it.
func MarkerPath(dataDir string) string { return filepath.Join(dataDir, markerFile) }

// LogPath is where the helper's log ends up when the data is kept.
func LogPath(dataDir string) string { return filepath.Join(dataDir, logFile) }

// staleAfter is when a marker no helper cleared is given up: the helper has
// long stopped waiting by then.
const staleAfter = time.Hour

// Pending reports whether dataDir's wasync is stopping to be removed, so the
// wasync app quits when it stops instead of waiting to start it again. A
// marker older than staleAfter is removed instead.
func Pending(dataDir string) bool {
	path := MarkerPath(dataDir)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if time.Since(info.ModTime()) > staleAfter {
		_ = os.Remove(path)
		return false
	}
	return true
}

// launch starts the helper, detached and working in dir; tests replace it.
var launch = startDetached

// Start records that wasync is being removed, so the wasync app quits when its
// wasync stops (see Pending), and starts the helper, detached, as
// `<exe> remove-apply REQUEST_FILE`. The caller then stops wasync. exe is the
// running wasync.
func Start(exe string, req Request) (err error) {
	if !filepath.IsAbs(req.DataDir) {
		return errors.New("the data directory must be an absolute path")
	}
	if !filepath.IsAbs(exe) {
		return errors.New("the wasync program must be an absolute path")
	}
	if err = req.Handoff.check(); err != nil {
		return err
	}
	if req.Created.IsZero() {
		req.Created = time.Now().UTC()
	}
	// EnsureDir creates the folder private from the start. A folder made by
	// os.MkdirTemp would inherit the temporary folder's access on Windows,
	// which EnsureDir then refuses.
	suffix := make([]byte, 8)
	if _, err = rand.Read(suffix); err != nil {
		return fmt.Errorf("could not make a folder for the removal: %w", err)
	}
	dir := filepath.Join(os.TempDir(), tempPrefix+hex.EncodeToString(suffix))
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
			_ = os.Remove(MarkerPath(req.DataDir))
		}
	}()
	if err = localfs.EnsureDir(dir); err != nil {
		return fmt.Errorf("could not make a folder for the removal: %w", err)
	}
	reqPath := filepath.Join(dir, requestFile)
	if err = writePrivateJSON(reqPath, req); err != nil {
		return fmt.Errorf("could not write the removal request: %w", err)
	}
	if err = writePrivateJSON(MarkerPath(req.DataDir), struct {
		Created time.Time `json:"created"`
	}{req.Created}); err != nil {
		return fmt.Errorf("could not record the removal: %w", err)
	}
	helper, err := helperExe(exe, dir)
	if err != nil {
		return fmt.Errorf("could not prepare the removal helper: %w", err)
	}
	if err = launch(helper, []string{ApplyCommand, reqPath}, dir, filepath.Join(dir, helperOut)); err != nil {
		return fmt.Errorf("could not start the removal helper: %w", err)
	}
	return nil
}

// writePrivateJSON writes v to a file only this user can read.
func writePrivateJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := localfs.OpenFile(path)
	if err != nil {
		return err
	}
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// readRequest reads a request Start wrote. It must be a private file, so
// nobody else on the computer can plant one.
func readRequest(path string) (Request, error) {
	var r Request
	if err := localfs.CheckPrivateFile(path); err != nil {
		return r, err
	}
	f, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if !filepath.IsAbs(r.DataDir) {
		return r, errors.New("the removal request names no data directory")
	}
	return r, r.Handoff.check()
}

// check refuses a hand-off that does not point at what it should.
func (h Handoff) check() error {
	switch h.Kind {
	case KindManual:
		return nil
	case KindMacApp:
		if !filepath.IsAbs(h.Target) || filepath.Ext(h.Target) != ".app" {
			return errors.New("the hand-off does not name the wasync app")
		}
	case KindWindowsSetup:
		if !filepath.IsAbs(h.Target) || !equalFold(filepath.Base(h.Target), uninstallerName) {
			return errors.New("the hand-off does not name the Setup uninstaller")
		}
	default:
		return fmt.Errorf("wasync cannot hand over to %q", h.Kind)
	}
	return nil
}
