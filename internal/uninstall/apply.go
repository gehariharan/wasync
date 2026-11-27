package uninstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/update"
)

// System is everything the helper does to the computer. DefaultSystem is the
// real one; tests replace every part, so they never touch a real login item,
// Keychain, Bin or uninstaller.
type System struct {
	// WaitOwner returns once no wasync holds dataDir, holding it so no other
	// wasync starts while the helper works. release lets it go again.
	WaitOwner func(ctx context.Context, dataDir string) (release func(), err error)
	// WaitExit returns once process pid has exited.
	WaitExit func(ctx context.Context, pid int) error
	// LoginItems are the start-at-login items to remove: the wasync app's own
	// item and the one `wasync service install` makes.
	LoginItems []service.Manager
	// ForgetApproval removes the approval credential kept for dataDir outside
	// it (the macOS Keychain item). Nothing to remove is not an error.
	ForgetApproval func(dataDir string) error
	// AxiCredential is where wasync-axi reads its credential file; the
	// per-app credential folders wasync makes sit beside it.
	AxiCredential func() (string, error)
	// RemoveAll deletes the data directory.
	RemoveAll func(path string) error
	// Trash moves a file or folder to the Bin (macOS).
	Trash func(path string) error
	// Run runs a command and waits for it (pkgutil, osascript on macOS).
	Run update.Runner
	// Launch starts a program without waiting for it (the Windows uninstaller).
	Launch func(exe string, args ...string) error
	// Now reads the clock; nil uses time.Now.
	Now func() time.Time
}

// The helper's bounded waits: long enough for wasync to finish cleanly and
// for the wasync app to quit, short enough that a stuck one does not keep the
// person waiting for ever.
var (
	ownerWait = 2 * time.Minute
	appWait   = 2 * time.Minute
)

// Apply is the helper's whole job. It reads the request Start wrote, waits for
// wasync and the wasync app to stop, removes the login items, deletes the data
// when asked to, and hands over to the platform's own removal. When wasync or
// the app is still running after its wait, it gives up and changes nothing,
// since removing a program or data in use could leave either broken. It logs
// each step: in the data directory when the data is kept, otherwise beside
// the request in the temporary folder. Once both have stopped, a step that
// fails is logged and the others still run, so the person is never left with
// half a removal they cannot see.
func Apply(ctx context.Context, requestPath string, s System) error {
	req, err := readRequest(requestPath)
	if err != nil {
		return fmt.Errorf("no removal of wasync is waiting: %w", err)
	}
	// Carried out once only, even if the helper is started again.
	_ = os.Remove(requestPath)
	log := newLog(filepath.Join(filepath.Dir(requestPath), logFile), s.Now)
	log.printf("Removing wasync for the data directory %s (delete data: %t).", req.DataDir, req.DeleteData)

	// giveUp ends a removal that cannot go ahead safely: nothing on the
	// computer is changed while wasync or the app may still run, and the
	// marker goes so the app and a later wasync start normally.
	giveUp := func(why string) error {
		log.printf("%s Nothing was removed; wasync and its data are as they were.", why)
		if err := os.Remove(MarkerPath(req.DataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.printf("Could not clear the removal note in the data directory: %v", err)
		}
		if err := log.moveTo(LogPath(req.DataDir)); err != nil {
			log.printf("Could not write this log to the data directory: %v", err)
		}
		return errors.Join(errors.New(why), log.close())
	}

	waitCtx, cancel := context.WithTimeout(ctx, ownerWait)
	release, err := s.WaitOwner(waitCtx, req.DataDir)
	cancel()
	if err != nil {
		return giveUp(fmt.Sprintf("wasync did not stop within %s (%v), so the removal was given up.", ownerWait, err))
	}
	log.printf("wasync has stopped.")
	released := false
	letGo := func() {
		if !released {
			release()
			released = true
		}
	}
	defer letGo()

	if req.AppPID > 0 && s.WaitExit != nil {
		waitCtx, cancel := context.WithTimeout(ctx, appWait)
		err := s.WaitExit(waitCtx, req.AppPID)
		cancel()
		if err != nil {
			letGo()
			return giveUp(fmt.Sprintf("The wasync app (process %d) did not quit within %s (%v), so the removal was given up.", req.AppPID, appWait, err))
		}
		log.printf("The wasync app has quit.")
	}

	removeLoginItems(s.LoginItems, log)

	deleted := false
	if req.DeleteData {
		s.forgetKeys(req.DataDir, log)
		// The lock file is in the data directory, and Windows cannot delete a
		// file that is open.
		letGo()
		deleted = s.deleteData(req.DataDir, log)
	}
	if !deleted {
		// The data stays, so a later install of wasync must start normally.
		if err := os.Remove(MarkerPath(req.DataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.printf("Could not clear the removal note in the data directory: %v", err)
		}
		letGo()
	}

	handErr := s.handOff(ctx, req.Handoff, log)
	if !deleted {
		if err := log.moveTo(LogPath(req.DataDir)); err != nil {
			log.printf("Could not write this log to the data directory: %v", err)
		}
	}
	return errors.Join(handErr, log.close())
}

// removeLoginItems removes each start-at-login item that is installed.
func removeLoginItems(items []service.Manager, log *helperLog) {
	for _, item := range items {
		if item == nil {
			continue
		}
		on, err := item.Installed()
		if !on && err == nil {
			continue
		}
		what := describeItem(item)
		if err != nil {
			log.printf("Could not check %s: %v", what, err)
		}
		if err := item.Uninstall(); err != nil {
			log.printf("Could not remove %s: %v", what, err)
			continue
		}
		log.printf("Removed %s.", what)
	}
}

func describeItem(m service.Manager) string {
	where := m.Where()
	if where == "" {
		where = "a start-at-login item"
	}
	if spec, err := service.Read(m); err == nil {
		return where + " (" + spec.Exe + ")"
	}
	return where
}

// forgetKeys removes what wasync keeps for the data directory outside it. It
// runs before the data directory is deleted, because telling wasync's own
// agent credentials apart needs the records inside it.
func (s System) forgetKeys(dataDir string, log *helperLog) {
	if s.ForgetApproval != nil {
		if err := s.ForgetApproval(dataDir); err != nil {
			log.printf("Could not remove the approval credential from the Keychain: %v", err)
		} else {
			log.printf("Removed the stored approval credential, if there was one.")
		}
	}
	if s.AxiCredential == nil {
		return
	}
	path, err := s.AxiCredential()
	if err != nil {
		log.printf("Could not find wasync-axi's credential folder: %v", err)
		return
	}
	for _, line := range removeAgentCredentials(dataDir, path) {
		log.printf("%s", line)
	}
}

// deleteData deletes the data directory, refusing anything that does not look
// like the one Start recorded the removal in.
func (s System) deleteData(dataDir string, log *helperLog) bool {
	if err := safeToDelete(dataDir); err != nil {
		log.printf("Kept the data directory %s: %v", dataDir, err)
		return false
	}
	if err := s.RemoveAll(dataDir); err != nil {
		log.printf("Could not delete all of the data directory %s: %v", dataDir, err)
		return false
	}
	log.printf("Deleted the data directory %s.", dataDir)
	return true
}

// safeToDelete checks dir is a real, absolute folder that is not the root or
// the home folder, and that it holds the marker Start wrote, so a mistaken
// request can never delete anything else.
func safeToDelete(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("it is not a plain absolute path")
	}
	if dir == filepath.VolumeName(dir)+string(filepath.Separator) {
		return errors.New("it is the root of a drive")
	}
	if home, err := os.UserHomeDir(); err == nil && equalPath(home, dir) {
		return errors.New("it is the home folder")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("it is not a real folder")
	}
	if m, err := os.Lstat(MarkerPath(dir)); err != nil || !m.Mode().IsRegular() {
		return errors.New("it does not hold the note wasync wrote when the removal started")
	}
	return nil
}

func equalPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || (filepath.Separator == '\\' && strings.EqualFold(a, b))
}

// handOff starts the platform's own removal of the program files.
func (s System) handOff(ctx context.Context, h Handoff, log *helperLog) error {
	switch h.Kind {
	case KindWindowsSetup:
		if s.Launch == nil {
			return errors.New("wasync cannot start the uninstaller here")
		}
		// Not silent: the person sees Setup's own "Are you sure" question, and
		// the uninstaller removes the program files and its own entry in
		// Settings > Apps.
		if err := s.Launch(h.Target); err != nil {
			log.printf("Could not open the uninstaller %s: %v. Uninstall wasync from Settings > Apps.", h.Target, err)
			return err
		}
		log.printf("Opened the uninstaller %s.", h.Target)
	case KindMacApp:
		return s.removeMacApp(ctx, h.Target, log)
	default:
		if h.Note != "" {
			log.printf("Program files are left for you to remove: %s", h.Note)
		}
	}
	return nil
}

// helperLog writes timestamped lines to a file as the helper goes, so a
// helper that is ended part-way still leaves a record, and keeps them so they
// can be moved to the data directory at the end.
type helperLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	buf  bytes.Buffer
	now  func() time.Time
}

func newLog(path string, now func() time.Time) *helperLog {
	if now == nil {
		now = time.Now
	}
	l := &helperLog{path: path, now: now}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		l.f = f
	}
	return l
}

func (l *helperLog) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := l.now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n"
	l.buf.WriteString(line)
	if l.f != nil {
		_, _ = io.WriteString(l.f, line)
	}
}

// moveTo appends everything logged so far to path and logs there from now
// on, removing the temporary log.
func (l *helperLog) moveTo(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(l.buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if l.f != nil {
		_ = l.f.Close()
		_ = os.Remove(l.path)
	}
	l.f, l.path = f, path
	return nil
}

func (l *helperLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// String is everything logged, for tests.
func (l *helperLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
