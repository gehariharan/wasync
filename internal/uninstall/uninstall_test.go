package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/update"
)

// isolateTemp points the system's temporary folder at a test folder, so the
// helper's request and log never land in the real one.
func isolateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, dir)
	}
	return dir
}

func dataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

type started struct {
	helper, dir, out string
	args             []string
}

// fakeLaunch records the helper Start would run, instead of running it.
func fakeLaunch(t *testing.T, err error) *started {
	t.Helper()
	got := &started{}
	old := launch
	launch = func(helper string, args []string, dir, out string) error {
		got.helper, got.args, got.dir, got.out = helper, args, dir, out
		return err
	}
	t.Cleanup(func() { launch = old })
	return got
}

func TestStartWritesAPrivateRequestOutsideTheDataDirectoryAndMarksTheRemoval(t *testing.T) {
	tmp := isolateTemp(t)
	dir := dataDir(t)
	got := fakeLaunch(t, nil)
	exe := filepath.Join(t.TempDir(), "wasync")
	if err := os.WriteFile(exe, []byte("program"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{DataDir: dir, DeleteData: true, AppPID: 42, Handoff: Handoff{Kind: KindManual, Note: "Delete it."}}
	if Pending(dir) {
		t.Fatal("nothing is being removed yet")
	}
	if err := Start(exe, req); err != nil {
		t.Fatal(err)
	}
	if !Pending(dir) {
		t.Fatal("the wasync app must see that wasync is stopping to be removed")
	}
	if len(got.args) != 2 || got.args[0] != ApplyCommand {
		t.Fatalf("helper started as %q", got.args)
	}
	reqPath := got.args[1]
	if rel, err := filepath.Rel(tmp, reqPath); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("the request %s must be in the temporary folder %s", reqPath, tmp)
	}
	if strings.HasPrefix(reqPath, dir) {
		t.Fatal("the request must not be in the data directory, which the helper may delete")
	}
	if got.dir != filepath.Dir(reqPath) {
		t.Fatalf("the helper must work in its own folder, not %s", got.dir)
	}
	if err := localfs.CheckPrivateFile(reqPath); err != nil {
		t.Fatalf("the request must be private: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(got.helper, "wasync-remove.exe") || filepath.Dir(got.helper) != got.dir {
			t.Fatalf("on Windows the helper runs from a copy in its folder, not %s", got.helper)
		}
	} else if got.helper != exe {
		t.Fatalf("helper %s, want %s", got.helper, exe)
	}
	back, err := readRequest(reqPath)
	if err != nil {
		t.Fatal(err)
	}
	if back.DataDir != dir || !back.DeleteData || back.AppPID != 42 || back.Handoff != req.Handoff || back.Created.IsZero() {
		t.Fatalf("request read back as %+v", back)
	}
}

func TestStartThatCannotStartTheHelperLeavesNothingBehind(t *testing.T) {
	tmp := isolateTemp(t)
	dir := dataDir(t)
	fakeLaunch(t, errors.New("no"))
	if err := Start(filepath.Join(t.TempDir(), "wasync"), Request{DataDir: dir, Handoff: Handoff{Kind: KindManual}}); err == nil {
		t.Fatal("a helper that did not start must be reported")
	}
	if Pending(dir) {
		t.Fatal("no helper runs, so the app must not quit for one")
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

func TestStartRefusesARequestThatPointsNowhere(t *testing.T) {
	isolateTemp(t)
	fakeLaunch(t, nil)
	dir := dataDir(t)
	exe := filepath.Join(t.TempDir(), "wasync")
	for _, req := range []Request{
		{DataDir: "relative", Handoff: Handoff{Kind: KindManual}},
		{DataDir: dir, Handoff: Handoff{Kind: KindMacApp, Target: "/Users/x/Documents"}},
		{DataDir: dir, Handoff: Handoff{Kind: KindWindowsSetup, Target: filepath.Join(dir, "evil.exe")}},
		{DataDir: dir, Handoff: Handoff{Kind: "format-disk"}},
	} {
		if err := Start(exe, req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
	if Pending(dir) {
		t.Fatal("a refused request must not mark a removal")
	}
}

func TestAMarkerLeftFromHoursAgoIsNoRemovalInProgress(t *testing.T) {
	dir := dataDir(t)
	if err := writePrivateJSON(MarkerPath(dir), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(MarkerPath(dir), old, old); err != nil {
		t.Fatal(err)
	}
	if Pending(dir) {
		t.Fatal("a marker no helper cleared hours ago must not quit the app")
	}
	if _, err := os.Stat(MarkerPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stale marker must be removed")
	}
}

// fakeItem is a login item that only remembers being removed.
type fakeItem struct {
	mu        sync.Mutex
	installed bool
	removed   int
}

func (f *fakeItem) Where() string { return "a fake login item" }
func (f *fakeItem) Install(service.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = true
	return nil
}
func (f *fakeItem) Uninstall() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = false
	f.removed++
	return nil
}
func (f *fakeItem) Installed() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed, nil
}

// world is a System made of fakes, and what they saw.
type world struct {
	sys      System
	app      *fakeItem
	legacy   *fakeItem
	trashed  []string
	launched []string
	ran      []string
	forgot   []string
	waited   []int
	released int
	axiPath  string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{app: &fakeItem{installed: true}, legacy: &fakeItem{installed: true}}
	w.axiPath = filepath.Join(t.TempDir(), "wasync-axi", "credential")
	w.sys = System{
		WaitOwner: func(context.Context, string) (func(), error) {
			return func() { w.released++ }, nil
		},
		WaitExit: func(_ context.Context, pid int) error {
			w.waited = append(w.waited, pid)
			return nil
		},
		LoginItems:     []service.Manager{w.app, w.legacy},
		ForgetApproval: func(dir string) []string { w.forgot = append(w.forgot, dir); return nil },
		AxiCredential:  func() (string, error) { return w.axiPath, nil },
		RemoveAll:      os.RemoveAll,
		Trash:          func(p string) error { w.trashed = append(w.trashed, p); return nil },
		Run: func(_ context.Context, name string, args []string, _ []string) ([]byte, []byte, error) {
			w.ran = append(w.ran, name+" "+strings.Join(args, " "))
			return nil, nil, errors.New("not installed")
		},
		Launch: func(exe string, args ...string) error {
			w.launched = append(w.launched, exe)
			return nil
		},
	}
	return w
}

// request writes a request the way Start does, and the marker beside the data.
func request(t *testing.T, req Request) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), tempPrefix+"test")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, requestFile)
	if err := writePrivateJSON(path, req); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(MarkerPath(req.DataDir), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeepingTheDataRemovesTheLoginItemsAndMovesTheAppToTheBin(t *testing.T) {
	w := newWorld(t)
	dir := dataDir(t)
	app := filepath.Join(t.TempDir(), "wasync.app")
	reqPath := request(t, Request{DataDir: dir, AppPID: 4242, Handoff: Handoff{Kind: KindMacApp, Target: app}})
	if err := Apply(context.Background(), reqPath, w.sys); err != nil {
		t.Fatal(err)
	}
	if w.app.removed != 1 || w.legacy.removed != 1 {
		t.Fatalf("both login items must go: app %d, legacy %d", w.app.removed, w.legacy.removed)
	}
	if len(w.waited) != 1 || w.waited[0] != 4242 {
		t.Fatalf("the helper must wait for the wasync app to quit: %v", w.waited)
	}
	if len(w.trashed) != 1 || w.trashed[0] != app {
		t.Fatalf("trashed %v, want the app", w.trashed)
	}
	if len(w.forgot) != 0 {
		t.Fatal("keeping the data keeps its keys")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the data must be kept: %v", err)
	}
	if Pending(dir) {
		t.Fatal("with the data kept, a later install must start normally")
	}
	if w.released != 1 {
		t.Fatalf("the data directory must be let go once, not %d times", w.released)
	}
	if _, err := os.Stat(reqPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the request must be used once only")
	}
	logged, err := os.ReadFile(LogPath(dir))
	if err != nil {
		t.Fatalf("the log belongs in the kept data directory: %v", err)
	}
	for _, want := range []string{"Removed a fake login item", "Moved " + app + " to the Bin"} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(reqPath), logFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the temporary log must go once it is in the data directory")
	}
	if len(w.ran) != 1 || !strings.Contains(w.ran[0], "--pkg-info "+axiReceipt) {
		t.Fatalf("without the package's receipt nothing else is run: %v", w.ran)
	}
}

func TestDeletingTheDataRemovesItAndWasyncsOwnAgentCredentialsOnly(t *testing.T) {
	w := newWorld(t)
	dir := dataDir(t)
	other := dataDir(t)
	cred, _, err := agentauth.Grant(dir, agentauth.ConnectRequest("codex", nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := agentauth.Grant(other, agentauth.ConnectRequest("claude-code", nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	axiDir := filepath.Dir(w.axiPath)
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(w.axiPath, cred)
	codex := filepath.Join(axiDir, "agents", "codex", "credential")
	write(codex, cred)
	claude := filepath.Join(axiDir, "agents", "claude-code", "credential")
	write(claude, foreign)
	notes := filepath.Join(axiDir, "agents", "claude-desktop", "notes.txt")
	write(notes, "the person's own file")
	config := filepath.Join(axiDir, "config.json")
	write(config, "{}")

	reqPath := request(t, Request{DataDir: dir, DeleteData: true, Handoff: Handoff{Kind: KindManual, Note: "Delete the program."}})
	if err := Apply(context.Background(), reqPath, w.sys); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the data directory must be deleted: %v", err)
	}
	if len(w.forgot) != 1 || w.forgot[0] != dir {
		t.Fatalf("the approval credential kept for the data directory must go: %v", w.forgot)
	}
	for _, gone := range []string{w.axiPath, codex, filepath.Dir(codex)} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s must be removed: %v", gone, err)
		}
	}
	for _, kept := range []string{claude, notes, config, other} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s is not this data directory's and must stay: %v", kept, err)
		}
	}
	logged, err := os.ReadFile(filepath.Join(filepath.Dir(reqPath), logFile))
	if err != nil {
		t.Fatalf("with the data deleted, the log stays in the temporary folder: %v", err)
	}
	for _, want := range []string{"Deleted the data directory", "Removed the agent credential " + codex, "Delete the program."} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("log lacks %q:\n%s", want, logged)
		}
	}
	if len(w.trashed)+len(w.launched) != 0 {
		t.Fatal("a manual hand-off removes no program files")
	}
}

func TestNothingIsRemovedWhileWasyncOrTheAppStillRuns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stuck func(w *world)
		why   string
	}{
		{"wasync does not stop", func(w *world) {
			w.sys.WaitOwner = func(context.Context, string) (func(), error) { return nil, context.DeadlineExceeded }
		}, "wasync did not stop"},
		{"the app does not quit", func(w *world) {
			w.sys.WaitExit = func(context.Context, int) error { return context.DeadlineExceeded }
		}, "The wasync app (process 7) did not quit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			tc.stuck(w)
			dir := dataDir(t)
			app := filepath.Join(t.TempDir(), "wasync.app")
			reqPath := request(t, Request{DataDir: dir, DeleteData: true, AppPID: 7, Handoff: Handoff{Kind: KindMacApp, Target: app}})
			if err := Apply(context.Background(), reqPath, w.sys); err == nil {
				t.Fatal("a removal that was given up must be reported")
			}
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("data a running wasync may use must not be deleted: %v", err)
			}
			if w.app.removed+w.legacy.removed != 0 || len(w.forgot) != 0 || len(w.trashed) != 0 || len(w.launched) != 0 || len(w.ran) != 0 {
				t.Fatalf("nothing may change: items %d/%d, keys %v, trashed %v, launched %v, ran %v", w.app.removed, w.legacy.removed, w.forgot, w.trashed, w.launched, w.ran)
			}
			if Pending(dir) {
				t.Fatal("the marker must be cleared, so the app and a later wasync start normally")
			}
			logged, _ := os.ReadFile(LogPath(dir))
			if !strings.Contains(string(logged), tc.why) || !strings.Contains(string(logged), "Nothing was removed") {
				t.Fatalf("the log must say why nothing was removed:\n%s", logged)
			}
		})
	}
}

func TestOnlyTheFolderTheRemovalWasRecordedInIsDeleted(t *testing.T) {
	dir := dataDir(t)
	if err := safeToDelete(dir); err == nil {
		t.Fatal("a folder without the removal marker must not be deleted")
	}
	if err := writePrivateJSON(MarkerPath(dir), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := safeToDelete(dir); err != nil {
		t.Fatalf("the marked data directory may be deleted: %v", err)
	}
	if err := safeToDelete(dir + string(filepath.Separator) + "." + string(filepath.Separator)); err == nil {
		t.Fatal("only a plain path may be deleted")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := safeToDelete(home); err == nil {
			t.Fatal("the home folder must never be deleted")
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		if err := safeToDelete(link); err == nil {
			t.Fatal("a link to a folder must not be deleted as one")
		}
	}
}

func TestWindowsSetupHandsOverToItsOwnUninstaller(t *testing.T) {
	w := newWorld(t)
	dir := dataDir(t)
	unins := filepath.Join(t.TempDir(), "unins000.exe")
	reqPath := request(t, Request{DataDir: dir, Handoff: Handoff{Kind: KindWindowsSetup, Target: unins}})
	if err := Apply(context.Background(), reqPath, w.sys); err != nil {
		t.Fatal(err)
	}
	if len(w.launched) != 1 || w.launched[0] != unins {
		t.Fatalf("launched %v, want the uninstaller", w.launched)
	}
}

func TestARequestThatIsNotPrivateIsNotCarriedOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows files have no permission bits; the folder's access list is the check")
	}
	w := newWorld(t)
	dir := dataDir(t)
	reqPath := request(t, Request{DataDir: dir, DeleteData: true, Handoff: Handoff{Kind: KindManual}})
	if err := os.Chmod(reqPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), reqPath, w.sys); err == nil {
		t.Fatal("a request others could have written must be refused")
	}
	if _, err := os.Stat(dir); err != nil || w.app.removed != 0 {
		t.Fatal("nothing may be changed for a refused request")
	}
}

func TestThePackagesWasyncAxiIsRemovedWithTheSystemsOwnPasswordDialog(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "wasync-axi")
	if err := os.WriteFile(plain, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "wasync-axi")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(plain, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		stat    string
		receipt bool
		files   string
		admin   bool
	}{
		{"installed by the package", plain, true, "usr/local/bin/wasync-axi\nusr/local/bin\n", true},
		{"no receipt", plain, false, "", false},
		{"receipt lists something else", plain, true, "usr/local/bin/other\n", false},
		{"a link another method owns", link, true, "usr/local/bin/wasync-axi\n", false},
		{"already gone", filepath.Join(t.TempDir(), "missing"), true, "usr/local/bin/wasync-axi\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stat == link && runtime.GOOS == "windows" {
				t.Skip("no symbolic links")
			}
			old := statAxi
			statAxi = func() (os.FileInfo, error) { return os.Lstat(tc.stat) }
			t.Cleanup(func() { statAxi = old })
			var ran []string
			var adminArgs []string
			s := System{Run: func(_ context.Context, name string, args []string, _ []string) ([]byte, []byte, error) {
				ran = append(ran, name)
				switch {
				case strings.HasSuffix(name, "pkgutil") && args[0] == "--pkg-info":
					if !tc.receipt {
						return nil, nil, errors.New("No receipt")
					}
				case strings.HasSuffix(name, "pkgutil") && args[0] == "--files":
					return []byte(tc.files), nil, nil
				case strings.HasSuffix(name, "osascript"):
					adminArgs = args
				}
				return nil, nil, nil
			}}
			log := newLog(filepath.Join(t.TempDir(), "log"), nil)
			defer log.close()
			s.removePackageAxi(context.Background(), log)
			if got := adminArgs != nil; got != tc.admin {
				t.Fatalf("asked for the administrator password: %v, want %v (ran %v)\n%s", got, tc.admin, ran, log)
			}
			if !tc.admin {
				return
			}
			script := adminArgs[len(adminArgs)-2]
			for _, want := range []string{"rm -f " + axiCommand, "[ ! -L " + axiCommand + " ]", "pkgutil --forget " + axiReceipt} {
				if !strings.Contains(script, want) {
					t.Errorf("script lacks %q:\n%s", want, script)
				}
			}
			if !strings.Contains(strings.Join(adminArgs, " "), "with administrator privileges") {
				t.Errorf("must use the system's administrator dialog: %q", adminArgs)
			}
		})
	}
}

func TestEachInstallIsRemovedTheWayItWasInstalled(t *testing.T) {
	for _, tc := range []struct {
		ch     update.Channel
		exe    string
		goos   string
		kind   string
		target string
		note   string
	}{
		{update.Channel{Kind: update.KindMacApp, OneClick: true, Target: "/Applications/wasync.app"}, "/Applications/wasync.app/Contents/MacOS/wasync", "darwin", KindMacApp, "/Applications/wasync.app", ""},
		{update.Channel{Kind: update.KindMacApp}, "/Applications/wasync.app/Contents/MacOS/wasync", "darwin", KindManual, "", "Drag wasync from there to the Bin"},
		{update.Channel{Kind: update.KindMacApp}, "/private/var/folders/x/AppTranslocation/1/d/wasync.app/Contents/MacOS/wasync", "darwin", KindManual, "", "temporary copy"},
		{update.Channel{Kind: update.KindWindowsSetup, OneClick: true, Target: filepath.Join("C:", "wasync", "wasync.exe")}, filepath.Join("C:", "wasync", "wasync.exe"), "windows", KindWindowsSetup, filepath.Join("C:", "wasync", "unins000.exe"), ""},
		{update.Channel{Kind: update.KindHomebrew}, "/opt/homebrew/Caskroom/wasync/1/wasync.app/Contents/MacOS/wasync", "darwin", KindManual, "", "brew uninstall --cask gehariharan/tap/wasync"},
		{update.Channel{Kind: update.KindHomebrew}, "/home/linuxbrew/.linuxbrew/bin/wasync", "linux", KindManual, "", "brew uninstall gehariharan/tap/wasync-axi gehariharan/tap/wasync"},
		{update.Channel{Kind: update.KindLinux}, "/home/a/.local/bin/wasync", "linux", KindManual, "", "install.sh"},
		{update.Channel{Kind: update.KindStore}, `C:\Program Files\WindowsApps\wasync.exe`, "windows", KindManual, "", "Microsoft Store"},
		{update.Channel{Kind: update.KindWindowsZIP}, `C:\tools\wasync\wasync.exe`, "windows", KindManual, "", "ZIP"},
		{update.Channel{Kind: update.KindMacCommand}, "/usr/local/bin/wasync", "darwin", KindManual, "", "macOS package"},
		{update.Channel{Kind: update.KindDevelopment}, "/src/wasync", "darwin", KindManual, "", "development build"},
	} {
		h := handoffFor(tc.ch, tc.exe, tc.goos)
		if h.Kind != tc.kind || h.Target != tc.target || !strings.Contains(h.Note, tc.note) {
			t.Errorf("%s at %s: %+v", tc.ch.Kind, tc.exe, h)
		}
		if strings.ContainsRune(h.Note, '\u2014') {
			t.Errorf("note uses a long dash: %q", h.Note)
		}
		if h.Kind != KindManual {
			if err := h.check(); err != nil && filepath.IsAbs(h.Target) {
				t.Errorf("%+v fails its own check: %v", h, err)
			}
		} else if h.Note == "" {
			t.Errorf("%s: a manual hand-off needs a note", tc.ch.Kind)
		}
	}
}

func TestRequestRoundTripsAsJSON(t *testing.T) {
	r := Request{DataDir: "/d", DeleteData: true, AppPID: 7, Handoff: Handoff{Kind: KindMacApp, Target: "/Applications/wasync.app"}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Request
	if err := json.Unmarshal(b, &back); err != nil || back != r {
		t.Fatalf("%+v %v", back, err)
	}
}

// keychainItems is a Keychain holding items for data directories; deleting one
// in foreign fails as macOS does for an item another program made.
type keychainItems struct {
	folders map[string]string
	foreign map[string]bool
}

func (k *keychainItems) Get(string) (string, error)       { return "", approvecred.ErrNotFound }
func (k *keychainItems) Set(string, string, string) error { return nil }
func (k *keychainItems) Delete(account string) error {
	if k.foreign[account] {
		return fmt.Errorf("%w (OSStatus -25293)", approvecred.ErrForeignItem)
	}
	delete(k.folders, account)
	return nil
}
func (k *keychainItems) List() ([]approvecred.Item, error) {
	var items []approvecred.Item
	for a, f := range k.folders {
		items = append(items, approvecred.Item{Account: a, Folder: f})
	}
	return items, nil
}

// An item another program made for the same data directory cannot be removed
// without asking; uninstall still reports the release's own credential as
// removed and says which item to delete in Keychain Access.
func TestForgettingTheApprovalCredentialNamesAnItemAnotherProgramMade(t *testing.T) {
	dir := t.TempDir()
	own, err := approvecred.Account(dir)
	if err != nil {
		t.Fatal(err)
	}
	const other = "approve:made-by-another-program"
	k := &keychainItems{folders: map[string]string{own: dir, other: dir}, foreign: map[string]bool{other: true}}
	approvecred.Use(k)
	t.Cleanup(func() { approvecred.Use(nil) })

	said := strings.Join(forgetApproval(dir), "\n")
	if _, kept := k.folders[own]; kept {
		t.Fatal("the release's own item was not removed")
	}
	if !strings.Contains(said, "Removed the stored approval credential") || !strings.Contains(said, other) || !strings.Contains(said, "Keychain Access") {
		t.Fatalf("uninstall said:\n%s", said)
	}
	if strings.Contains(said, "Could not remove the approval credential") || strings.Contains(said, approvecred.ErrAccessDenied.Error()) {
		t.Fatalf("the foreign item was reported as a failure to remove the credential:\n%s", said)
	}
}
