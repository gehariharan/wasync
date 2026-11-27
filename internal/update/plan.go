package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// The update folder sits in the private data directory, so no other account
// can swap a checked file before it is installed.
const (
	folderName = "update"
	planFile   = "plan.json"
	resultFile = "result.json"
	// stagingPrefix names the per-update folders that hold downloads.
	stagingPrefix = "staging-"
)

// Folder is the update folder of a data directory.
func Folder(dataDir string) string { return filepath.Join(dataDir, folderName) }

// PlanPath is the update plan the helper installs; it exists from the moment
// wasync stops for an update until the helper has finished.
func PlanPath(dataDir string) string { return filepath.Join(Folder(dataDir), planFile) }

// Relaunch says how the helper starts wasync again after installing.
type Relaunch struct {
	// How is one of the Relaunch* values.
	How string `json:"how"`
	// Exe and Args are the command to run again for RelaunchCommand.
	Exe  string   `json:"exe,omitempty"`
	Args []string `json:"args,omitempty"`
	// App is the .app bundle to open for RelaunchApp.
	App string `json:"app,omitempty"`
	// ServiceExe and DataDir describe the login item for RelaunchLoginItem.
	ServiceExe string `json:"service_exe,omitempty"`
	DataDir    string `json:"data_dir,omitempty"`
}

const (
	// RelaunchLoginItem starts the login item again (it started this copy).
	RelaunchLoginItem = "login-item"
	// RelaunchCommand runs the same wasync command again in the background.
	RelaunchCommand = "command"
	// RelaunchApp opens the macOS app again.
	RelaunchApp = "app"
	// RelaunchNone leaves starting wasync to the person (a terminal UI).
	RelaunchNone = "none"
)

// Plan is what the helper does once the running wasync has stopped.
type Plan struct {
	Kind        string    `json:"kind"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Target      string    `json:"target"`
	Staged      string    `json:"staged"`
	SHA256      string    `json:"sha256,omitempty"`
	Requirement string    `json:"requirement,omitempty"`
	Relaunch    Relaunch  `json:"relaunch"`
	Created     time.Time `json:"created"`
}

// Result is how the last update ended. The helper writes it; the next wasync
// shows it.
type Result struct {
	From string `json:"from"`
	To   string `json:"to"`
	OK   bool   `json:"ok"`
	// Error says why the update was not installed.
	Error string `json:"error,omitempty"`
	// Restart says whether and how wasync was started again.
	Restart  string    `json:"restart,omitempty"`
	Finished time.Time `json:"finished"`
}

func writeJSONFile(dir, name string, v any) error {
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func readJSONFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// LastResult reads how the last update ended; nil when none has run.
func LastResult(dataDir string) (*Result, error) {
	var r Result
	if err := readJSONFile(filepath.Join(Folder(dataDir), resultFile), &r); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// staleAfter is when a plan no helper finished is given up: the helper has
// long stopped waiting for wasync by then.
const staleAfter = time.Hour

// pending reports whether a helper has a plan it has not finished. A plan
// older than staleAfter is removed instead.
func pending(dataDir string) bool {
	path := filepath.Join(Folder(dataDir), planFile)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) > staleAfter {
		_ = os.Remove(path)
		return false
	}
	return true
}

// cleanStaging removes download folders no plan refers to, such as one left
// by a wasync that stopped in the middle of a download.
func cleanStaging(dataDir string) {
	if pending(dataDir) {
		return
	}
	entries, err := os.ReadDir(Folder(dataDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) || strings.HasPrefix(e.Name(), "helper-") {
			_ = os.RemoveAll(filepath.Join(Folder(dataDir), e.Name()))
		}
	}
}

// ApplyDeps is what the helper needs from the rest of wasync.
type ApplyDeps struct {
	Platform Platform
	// WaitOwner returns once the running wasync has let go of the data
	// directory, holding it so no other wasync starts during the install.
	// release lets go of it again.
	WaitOwner func(ctx context.Context) (release func(), err error)
	// Start starts wasync again as r says.
	Start func(r Relaunch) error
	Now   func() time.Time
}

// ownerWait bounds how long the helper waits for wasync to stop.
const ownerWait = 2 * time.Minute

// Apply is the helper's whole job: wait for the running wasync to stop,
// install the planned update, start wasync again (the old version when the
// install failed, so the person is never left without it), and record how it
// went for the next wasync to show.
func Apply(ctx context.Context, dataDir string, d ApplyDeps) error {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	dir := Folder(dataDir)
	var p Plan
	if err := readJSONFile(filepath.Join(dir, planFile), &p); err != nil {
		return fmt.Errorf("no update is waiting to be installed: %w", err)
	}
	result := Result{From: p.From, To: p.To}
	// finish records the outcome; restart is false only while the old wasync
	// is still running, when starting another would be refused anyway.
	finish := func(installErr error, restart bool) error {
		if installErr != nil {
			result.Error = installErr.Error()
		} else {
			result.OK = true
		}
		switch {
		case !restart:
			result.Restart = "wasync is still running the version it had."
		case p.Relaunch.How == RelaunchNone || d.Start == nil:
			result.Restart = restartWords(RelaunchNone)
		default:
			result.Restart = restartWords(p.Relaunch.How)
			if err := d.Start(p.Relaunch); err != nil {
				result.Restart = "wasync could not be started again (" + err.Error() + "). Open it yourself."
			}
		}
		result.Finished = now().UTC()
		writeErr := writeJSONFile(dir, resultFile, result)
		_ = os.Remove(filepath.Join(dir, planFile))
		if root := stagingRoot(dir, p.Staged); root != "" && strings.HasPrefix(filepath.Base(root), stagingPrefix) {
			_ = os.RemoveAll(root)
		}
		return errors.Join(installErr, writeErr)
	}
	waitCtx, cancel := context.WithTimeout(ctx, ownerWait)
	release, err := d.WaitOwner(waitCtx)
	cancel()
	if err != nil {
		return finish(fmt.Errorf("wasync did not stop, so the update was not installed: %w", err), false)
	}
	installErr := p.check(dir)
	if installErr == nil {
		installErr = d.Platform.Install(ctx, p)
	}
	release()
	return finish(installErr, true)
}

// stagingRoot is the staging-* folder directly inside dir that holds path.
func stagingRoot(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return filepath.Join(dir, first)
}

// check refuses a plan whose staged file is not one this wasync downloaded.
func (p Plan) check(dir string) error {
	root := stagingRoot(dir, p.Staged)
	if root == "" || !strings.HasPrefix(filepath.Base(root), stagingPrefix) || !filepath.IsAbs(p.Target) {
		return errors.New("the update plan does not point at a checked download, so nothing was installed")
	}
	switch p.Kind {
	case KindMacApp:
		if !strings.HasSuffix(p.Target, ".app") || p.Requirement == "" {
			return errors.New("the update plan is incomplete, so nothing was installed")
		}
	case KindWindowsSetup:
		if p.SHA256 == "" {
			return errors.New("the update plan is incomplete, so nothing was installed")
		}
	default:
		return fmt.Errorf("wasync cannot install a %q update", p.Kind)
	}
	return nil
}

func restartWords(how string) string {
	switch how {
	case RelaunchLoginItem:
		return "Started again by its login item."
	case RelaunchCommand:
		return "Started again in the background."
	case RelaunchApp:
		return "The app was opened again."
	}
	return "Start wasync again to use it."
}
