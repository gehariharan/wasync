package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

func TestNewerComparesPreviewsAndRefusesUnreadableVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0-preview.20261126111622", "0.1.0-preview.20261126105548", true},
		{"0.1.0-preview.20261126105548", "0.1.0-preview.20261126111622", false},
		{"0.1.0", "0.1.0-preview.20261126111622", true},
		{"0.1.0-preview.1", "0.1.0", false},
		{"v0.2.0", "0.1.9", true},
		{"0.1.10", "0.1.9", true},
		{"0.1.0-preview.2", "0.1.0-preview.10", false},
		{"0.1.0-beta", "0.1.0-alpha", true},
		{"0.1.0-alpha.1", "0.1.0-alpha", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0-preview.1", "dev", false},
		{"dev", "0.1.0", false},
		{"01.0.0", "0.1.0", false},
		{"1.0", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNewestSkipsDraftsAndOtherTags(t *testing.T) {
	list := []Release{
		{Tag: "v0.1.0-preview.30000000000000", Draft: true},
		{Tag: "axi/v0.1.0-preview.20261126111622"},
		{Tag: "v0.1.0-preview.20261126105548"},
		{Tag: "v0.1.0-preview.20261126111622"},
		{Tag: "nightly"},
	}
	if got := newest(list); got == nil || got.Tag != "v0.1.0-preview.20261126111622" {
		t.Fatalf("newest = %+v", got)
	}
	if newest(nil) != nil {
		t.Fatal("no releases should give nil")
	}
}

func TestChecksumsMatchTheReleaseListAndGitHubsDigest(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	sums := parseChecksums([]byte(sum + "  ./wasync_1.0.0_windows_amd64-setup.exe\n" + other + " *wasync_1.0.0_darwin_arm64.dmg\nnot a line\nzz  bad\n"))
	if sums["wasync_1.0.0_windows_amd64-setup.exe"] != sum || sums["wasync_1.0.0_darwin_arm64.dmg"] != other || len(sums) != 2 {
		t.Fatalf("parsed %v", sums)
	}
	a := Asset{Name: "wasync_1.0.0_windows_amd64-setup.exe"}
	if err := checkDigest(sum, a, sums); err != nil {
		t.Fatal(err)
	}
	if err := checkDigest(other, a, sums); err == nil || !strings.Contains(err.Error(), "does not match its published checksum") {
		t.Fatalf("a wrong file must be refused: %v", err)
	}
	if err := checkDigest(sum, Asset{Name: "missing.exe"}, sums); err == nil {
		t.Fatal("a file the list does not name must be refused")
	}
	a.Digest = "sha256:" + other
	if err := checkDigest(sum, a, sums); err == nil || !strings.Contains(err.Error(), "GitHub's digest") {
		t.Fatalf("a digest mismatch must be refused: %v", err)
	}
}

func TestDownloadsStayOnGitHub(t *testing.T) {
	g := GitHub{}
	for raw, want := range map[string]bool{
		"https://github.com/gehariharan/wasync/releases/download/v1/x.exe": true,
		"https://objects.githubusercontent.com/x":                          true,
		"https://release-assets.githubusercontent.com/x":                   true,
		"https://api.github.com/repos/gehariharan/wasync/releases":         true,
		"http://github.com/x":                          false,
		"https://evil.example/x":                       false,
		"https://githubusercontent.com.evil.example/x": false,
		"https://user:pw@github.com/x":                 false,
		"http://127.0.0.1:9/x":                         false,
	} {
		u, _ := url.Parse(raw)
		if got := g.allowed(u); got != want {
			t.Errorf("allowed(%s) = %v, want %v", raw, got, want)
		}
	}
	local := GitHub{ListURL: "http://127.0.0.1:4000/releases"}
	for raw, want := range map[string]bool{"http://127.0.0.1:4000/a": true, "http://127.0.0.1:4001/a": false, "https://github.com/x": false} {
		u, _ := url.Parse(raw)
		if got := local.allowed(u); got != want {
			t.Errorf("loopback allowed(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestGitHubListsAndDownloadsFromATestServer(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 200<<10)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v9.9.9","draft":true},{"tag_name":"v1.2.3","html_url":"https://github.com/gehariharan/wasync/releases/tag/v1.2.3","assets":[{"name":"a.exe","browser_download_url":"%s/dl/a.exe","size":%d}]}]`, srv.URL, len(payload))
		case "/dl/a.exe":
			http.Redirect(w, r, "/files/a.exe", http.StatusFound)
		case "/files/a.exe":
			w.Write(payload)
		case "/elsewhere":
			http.Redirect(w, r, "https://evil.example/a.exe", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := GitHub{ListURL: srv.URL + "/releases"}
	rel, err := g.Latest(context.Background())
	if err != nil || rel == nil || rel.Version() != "1.2.3" || len(rel.Assets) != 1 {
		t.Fatalf("latest: %+v %v", rel, err)
	}
	var buf bytes.Buffer
	var seen int64
	if err := g.Fetch(context.Background(), rel.Assets[0].URL, &buf, int64(len(payload)), func(n int64) { seen = n }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), payload) || seen != int64(len(payload)) {
		t.Fatalf("got %d bytes, progress %d", buf.Len(), seen)
	}
	if err := g.Fetch(context.Background(), rel.Assets[0].URL, io.Discard, 10, nil); err == nil {
		t.Fatal("a download over its size must be refused")
	}
	if err := g.Fetch(context.Background(), srv.URL+"/elsewhere", io.Discard, 10, nil); err == nil {
		t.Fatal("a redirect away from the test server must be refused")
	}
}

func TestDetectTellsInstallsApart(t *testing.T) {
	exists := func(set ...string) func(string) bool {
		return func(p string) bool {
			for _, s := range set {
				if p == s {
					return true
				}
			}
			return false
		}
	}
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	win := `C:\Users\me\AppData\Local\Programs\wasync\wasync.exe`
	cases := []struct {
		name     string
		host     Host
		kind     string
		oneClick bool
	}{
		{"dev", Host{GOOS: "darwin", Version: "dev", Exe: "/Applications/wasync.app/Contents/MacOS/wasync", Writable: yes}, KindDevelopment, false},
		{"mac app", Host{GOOS: "darwin", GOARCH: "arm64", Version: "1.0.0", Exe: "/Applications/wasync.app/Contents/MacOS/wasync", Writable: yes}, KindMacApp, true},
		{"mac app read-only folder", Host{GOOS: "darwin", Version: "1.0.0", Exe: "/Applications/wasync.app/Contents/MacOS/wasync", Writable: no}, KindMacApp, false},
		{"mac translocated", Host{GOOS: "darwin", Version: "1.0.0", Exe: "/private/var/folders/x/AppTranslocation/ABC/d/wasync.app/Contents/MacOS/wasync", Writable: yes}, KindMacApp, false},
		{"mac cask", Host{GOOS: "darwin", Version: "1.0.0", Exe: "/opt/homebrew/bin/wasync", Resolved: "/opt/homebrew/Caskroom/wasync/1.0.0/wasync"}, KindHomebrew, false},
		{"mac pkg", Host{GOOS: "darwin", Version: "1.0.0", Exe: "/usr/local/bin/wasync"}, KindMacCommand, false},
		{"linux brew", Host{GOOS: "linux", Version: "1.0.0", Exe: "/home/linuxbrew/.linuxbrew/Cellar/wasync/1.0.0/bin/wasync"}, KindHomebrew, false},
		{"linux archive", Host{GOOS: "linux", Version: "1.0.0", Exe: "/usr/local/bin/wasync"}, KindLinux, false},
		{"windows setup", Host{GOOS: "windows", GOARCH: "amd64", Version: "1.0.0", Exe: win, Exists: exists(filepath.Join(filepath.Dir(win), "unins000.exe"))}, KindWindowsSetup, true},
		{"windows zip", Host{GOOS: "windows", GOARCH: "amd64", Version: "1.0.0", Exe: win, Exists: no}, KindWindowsZIP, false},
		{"windows store", Host{GOOS: "windows", GOARCH: "amd64", Version: "1.0.0", Distribution: "msix", Exe: win, Exists: yes}, KindStore, false},
	}
	for _, c := range cases {
		ch := Detect(c.host)
		if ch.Kind != c.kind || ch.OneClick != c.oneClick {
			t.Errorf("%s: got %+v", c.name, ch)
		}
		if !ch.OneClick && ch.Note == "" {
			t.Errorf("%s: an install that cannot update itself must say how to update", c.name)
		}
	}
	mac := Detect(cases[1].host)
	if mac.Target != filepath.FromSlash("/Applications/wasync.app") {
		t.Fatalf("mac target %q", mac.Target)
	}
	if got := mac.AssetNames("1.0.0"); got[0] != "wasync_1.0.0_darwin_arm64.dmg" {
		t.Fatalf("mac asset %v", got)
	}
	if got := Detect(cases[8].host).AssetNames("1.0.0"); got[0] != "wasync_1.0.0_windows_amd64-setup.exe" {
		t.Fatalf("windows asset %v", got)
	}
}

// fakeSource serves a release from memory.
type fakeSource struct {
	release *Release
	files   map[string][]byte
	err     error
}

func (f *fakeSource) Latest(context.Context) (*Release, error) { return f.release, f.err }
func (f *fakeSource) Fetch(_ context.Context, rawURL string, w io.Writer, limit int64, progress func(int64)) error {
	b, ok := f.files[rawURL]
	if !ok {
		return errors.New("not found")
	}
	if int64(len(b)) > limit {
		return errors.New("too large")
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if progress != nil {
		progress(int64(len(b)))
	}
	return nil
}

// fakePlatform records what it was asked to do.
type fakePlatform struct {
	mu         sync.Mutex
	probed     int
	prepared   string
	prepareErr error
	installed  []Plan
	installErr error
}

func (p *fakePlatform) Probe(_ context.Context, ch *Channel) {
	p.mu.Lock()
	p.probed++
	p.mu.Unlock()
	ch.Team = "ABCDE12345"
}
func (p *fakePlatform) Prepare(_ context.Context, ch Channel, download, staging string) (Staged, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepared = download
	if p.prepareErr != nil {
		return Staged{}, p.prepareErr
	}
	sum, _ := fileSHA256(download)
	return Staged{Path: download, SHA256: sum, Requirement: ch.Team}, nil
}
func (p *fakePlatform) Install(_ context.Context, plan Plan) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.installed = append(p.installed, plan)
	return p.installErr
}

const setupName = "wasync_1.1.0_windows_amd64-setup.exe"

func releaseWith(payload []byte, listed string) (*Release, map[string][]byte) {
	files := map[string][]byte{"https://github.com/dl/setup.exe": payload, "https://github.com/dl/checksums.txt": []byte(listed + "  ./" + setupName + "\n")}
	return &Release{Tag: "v1.1.0", PageURL: "https://github.com/gehariharan/wasync/releases/tag/v1.1.0", Assets: []Asset{
		{Name: setupName, URL: "https://github.com/dl/setup.exe", Size: int64(len(payload))},
		{Name: ChecksumsAsset, URL: "https://github.com/dl/checksums.txt", Size: 100},
	}}, files
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type managerFixture struct {
	m        *Manager
	dir      string
	platform *fakePlatform
	launched chan string
	stopped  chan struct{}
}

func newManagerFixture(t *testing.T, src Source, ch Channel) managerFixture {
	t.Helper()
	f := managerFixture{dir: filepath.Join(t.TempDir(), "data"), platform: &fakePlatform{}, launched: make(chan string, 1), stopped: make(chan struct{}, 1)}
	if err := os.Mkdir(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.m = NewManager(context.Background(), Config{
		DataDir: f.dir, Current: "1.0.0", Channel: ch, Source: src, Platform: f.platform,
		Relaunch: Relaunch{How: RelaunchCommand, Exe: "/bin/wasync", Args: []string{"serve"}},
		Launch:   func(d string) error { f.launched <- d; return nil },
		Stop:     func() { f.stopped <- struct{}{} },
	})
	return f
}

var windowsChannel = Channel{Kind: KindWindowsSetup, OneClick: true, Target: filepath.Join(os.TempDir(), "wasync", "wasync.exe"), Arch: "amd64"}

func waitState(t *testing.T, m *Manager, want string) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := m.Status()
		if s.State == want {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %q, want %q (error %q)", s.State, want, s.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOneClickDownloadsChecksPlansAndHandsOver(t *testing.T) {
	payload := []byte("new setup program")
	rel, files := releaseWith(payload, sha(payload))
	f := newManagerFixture(t, &fakeSource{release: rel, files: files}, windowsChannel)
	if _, err := f.m.Start("1.1.0"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("starting before a check: %v", err)
	}
	s := f.m.Check(context.Background())
	if _, err := f.m.Start("1.2.0"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("starting a version nobody agreed to: %v", err)
	}
	if s.State != StateAvailable || !s.CanInstall || s.Latest.Version != "1.1.0" || s.Latest.Asset != setupName {
		t.Fatalf("check: %+v", s)
	}
	if f.platform.probed != 1 {
		t.Fatalf("probed %d times", f.platform.probed)
	}
	if _, err := f.m.Start("1.1.0"); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-f.launched:
		if d != f.dir {
			t.Fatalf("helper got %q", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("helper never started: %+v", f.m.Status())
	}
	<-f.stopped
	s = f.m.Status()
	if s.State != StateInstalling || s.Progress.Done != int64(len(payload)) {
		t.Fatalf("after hand-over: %+v", s)
	}
	var p Plan
	if err := readJSONFile(filepath.Join(Folder(f.dir), planFile), &p); err != nil {
		t.Fatal(err)
	}
	if p.From != "1.0.0" || p.To != "1.1.0" || p.Kind != KindWindowsSetup || p.SHA256 != sha(payload) || p.Relaunch.How != RelaunchCommand || p.check(Folder(f.dir)) != nil {
		t.Fatalf("plan %+v", p)
	}
	if _, err := f.m.Start("1.1.0"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second start: %v", err)
	}
}

func TestAWrongChecksumInstallsNothing(t *testing.T) {
	payload := []byte("tampered setup program")
	rel, files := releaseWith(payload, sha([]byte("the published one")))
	f := newManagerFixture(t, &fakeSource{release: rel, files: files}, windowsChannel)
	f.m.Check(context.Background())
	if _, err := f.m.Start("1.1.0"); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, f.m, StateFailed)
	if !strings.Contains(s.Error, "does not match its published checksum") {
		t.Fatalf("error %q", s.Error)
	}
	if f.platform.prepared != "" || len(f.launched) != 0 || len(f.stopped) != 0 {
		t.Fatal("nothing may be prepared, launched or stopped after a bad checksum")
	}
	entries, _ := os.ReadDir(Folder(f.dir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) || e.Name() == planFile {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

func TestAFailedSignatureCheckInstallsNothing(t *testing.T) {
	payload := []byte("setup program")
	rel, files := releaseWith(payload, sha(payload))
	f := newManagerFixture(t, &fakeSource{release: rel, files: files}, windowsChannel)
	f.platform.prepareErr = errors.New("the update is not signed by wasync's publisher")
	f.m.Check(context.Background())
	if _, err := f.m.Start("1.1.0"); err != nil {
		t.Fatal(err)
	}
	if s := waitState(t, f.m, StateFailed); !strings.Contains(s.Error, "publisher") {
		t.Fatalf("error %q", s.Error)
	}
	if len(f.launched) != 0 || len(f.stopped) != 0 {
		t.Fatal("a refused update must not stop wasync")
	}
}

func TestChecksSayWhyOneClickIsNotOffered(t *testing.T) {
	payload := []byte("x")
	rel, files := releaseWith(payload, sha(payload))
	brew := Channel{Kind: KindHomebrew, Note: "Update it with brew."}
	f := newManagerFixture(t, &fakeSource{release: rel, files: files}, brew)
	s := f.m.Check(context.Background())
	if s.State != StateAvailable || s.CanInstall || s.Note != "Update it with brew." {
		t.Fatalf("homebrew: %+v", s)
	}
	mac := Channel{Kind: KindMacApp, OneClick: true, Target: "/Applications/wasync.app", Arch: "arm64"}
	f = newManagerFixture(t, &fakeSource{release: rel, files: files}, mac)
	if s = f.m.Check(context.Background()); s.CanInstall || !strings.Contains(s.Note, "no download for this computer") {
		t.Fatalf("no disk image yet: %+v", s)
	}
	current := &Release{Tag: "v1.0.0"}
	f = newManagerFixture(t, &fakeSource{release: current}, windowsChannel)
	if s = f.m.Check(context.Background()); s.State != StateCurrent || s.CanInstall {
		t.Fatalf("up to date: %+v", s)
	}
	f = newManagerFixture(t, &fakeSource{err: errors.New("offline")}, windowsChannel)
	if s = f.m.Check(context.Background()); s.State != StateFailed || s.Error != "offline" {
		t.Fatalf("offline: %+v", s)
	}
}

func TestTheLastResultShowsOnlyWhileItDescribesTheRunningVersion(t *testing.T) {
	f := newManagerFixture(t, &fakeSource{release: &Release{Tag: "v1.0.0"}}, windowsChannel)
	for _, c := range []struct {
		r    Result
		show bool
	}{
		{Result{From: "0.9.0", To: "1.0.0", OK: true}, true},
		{Result{From: "1.0.0", To: "1.1.0", Error: "Setup did not finish"}, true},
		{Result{From: "0.8.0", To: "0.9.0", OK: true}, false},
		{Result{From: "0.9.0", To: "0.9.5", Error: "Setup did not finish"}, false},
		{Result{From: "0.9.0", To: "1.0.0", Error: "Setup did not finish"}, false},
	} {
		if err := writeJSONFile(Folder(f.dir), resultFile, c.r); err != nil {
			t.Fatal(err)
		}
		for name, s := range map[string]Status{"status": f.m.Status(), "check": f.m.Check(context.Background())} {
			if got := s.LastResult != nil; got != c.show {
				t.Errorf("%s on 1.0.0 with %+v: shown %v, want %v", name, c.r, got, c.show)
			}
		}
	}
}

// applyFixture is a data directory with a staged update and a plan.
func applyFixture(t *testing.T, kind string) (dir string, plan Plan) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "data")
	// Like Manager.start, make the update folder private before staging in it.
	if err := localfs.EnsureDir(Folder(dir)); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(Folder(dir), stagingPrefix+"1")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(staging, "setup.exe")
	if err := os.WriteFile(staged, []byte("setup"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan = Plan{Kind: kind, From: "1.0.0", To: "1.1.0", Target: filepath.Join(dir, "wasync.exe"), Staged: staged, SHA256: sha([]byte("setup")), Requirement: "req", Relaunch: Relaunch{How: RelaunchCommand, Exe: "/bin/wasync"}}
	if kind == KindMacApp {
		plan.Target = filepath.Join(dir, "wasync.app")
	}
	if err := writeJSONFile(Folder(dir), planFile, plan); err != nil {
		t.Fatal(err)
	}
	return dir, plan
}

func TestTheHelperInstallsRestartsAndRecordsTheResult(t *testing.T) {
	dir, _ := applyFixture(t, KindWindowsSetup)
	platform := &fakePlatform{}
	var order []string
	err := Apply(context.Background(), dir, ApplyDeps{
		Platform: platform,
		WaitOwner: func(context.Context) (func(), error) {
			order = append(order, "wait")
			return func() { order = append(order, "release") }, nil
		},
		Start: func(r Relaunch) error { order = append(order, "start:"+r.Exe); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(platform.installed) != 1 || strings.Join(order, ",") != "wait,release,start:/bin/wasync" {
		t.Fatalf("installed %d, order %v", len(platform.installed), order)
	}
	r, err := LastResult(dir)
	if err != nil || r == nil || !r.OK || r.From != "1.0.0" || r.To != "1.1.0" || !strings.Contains(r.Restart, "background") {
		t.Fatalf("result %+v %v", r, err)
	}
	if pending(dir) {
		t.Fatal("the plan must be removed once handled")
	}
	if _, err := os.Stat(filepath.Join(Folder(dir), stagingPrefix+"1")); !os.IsNotExist(err) {
		t.Fatal("the download must be removed once handled")
	}
}

func TestAFailedInstallStillStartsTheOldVersionAgain(t *testing.T) {
	dir, _ := applyFixture(t, KindWindowsSetup)
	platform := &fakePlatform{installErr: errors.New("Setup did not finish")}
	started := false
	err := Apply(context.Background(), dir, ApplyDeps{
		Platform:  platform,
		WaitOwner: func(context.Context) (func(), error) { return func() {}, nil },
		Start:     func(Relaunch) error { started = true; return nil },
	})
	if err == nil || !started {
		t.Fatalf("err %v, started %v", err, started)
	}
	if r, _ := LastResult(dir); r == nil || r.OK || !strings.Contains(r.Error, "Setup did not finish") {
		t.Fatalf("result %+v", r)
	}
}

func TestTheHelperInstallsNothingWhileWasyncKeepsRunning(t *testing.T) {
	dir, _ := applyFixture(t, KindWindowsSetup)
	platform := &fakePlatform{}
	started := false
	err := Apply(context.Background(), dir, ApplyDeps{
		Platform:  platform,
		WaitOwner: func(context.Context) (func(), error) { return nil, context.DeadlineExceeded },
		Start:     func(Relaunch) error { started = true; return nil },
	})
	if err == nil || started || len(platform.installed) != 0 {
		t.Fatalf("err %v, started %v, installed %d", err, started, len(platform.installed))
	}
	if r, _ := LastResult(dir); r == nil || r.OK || !strings.Contains(r.Restart, "still running") {
		t.Fatalf("result %+v", r)
	}
}

func TestTheHelperRefusesAPlanOutsideItsDownloads(t *testing.T) {
	dir, plan := applyFixture(t, KindWindowsSetup)
	plan.Staged = filepath.Join(t.TempDir(), "elsewhere.exe")
	if err := writeJSONFile(Folder(dir), planFile, plan); err != nil {
		t.Fatal(err)
	}
	platform := &fakePlatform{}
	err := Apply(context.Background(), dir, ApplyDeps{Platform: platform, WaitOwner: func(context.Context) (func(), error) { return func() {}, nil }, Start: func(Relaunch) error { return nil }})
	if err == nil || len(platform.installed) != 0 {
		t.Fatalf("err %v, installed %d", err, len(platform.installed))
	}
}
