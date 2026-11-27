package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/cli"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/firstopen"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/update"
)

// The test binary stands in for wasync: started with WASYNC_FAKE set, it
// speaks the app channel the way `wasync setup --app` does.
func TestMain(m *testing.M) {
	switch os.Getenv("WASYNC_FAKE") {
	case "":
		os.Exit(m.Run())
	case "fail":
		fmt.Fprintln(os.Stderr, "fake wasync: the archive is unavailable")
		os.Exit(3)
	case "busy":
		fmt.Fprintln(os.Stderr, "fake wasync: wasync setup cannot start: wasync serve already owns this data directory")
		os.Exit(1)
	case "hold":
		if err := holdLinked(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "fake wasync:", err)
			os.Exit(4)
		}
		fmt.Println("held")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	default:
		fakeWasync()
		os.Exit(0)
	}
}

func fakeWasync() {
	args := strings.Join(os.Args[1:], " ")
	enc := json.NewEncoder(os.Stdout)
	if !strings.Contains(args, "setup --app") || os.Getenv(cli.DesktopAppEnv) == "" {
		fmt.Fprintln(os.Stderr, "fake wasync: started without setup --app or the app's program:", args)
		os.Exit(2)
	}
	mode := os.Getenv("WASYNC_FAKE")
	if mode == "linked" {
		if err := holdLinked(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "fake wasync:", err)
			os.Exit(4)
		}
	}
	_ = enc.Encode(cli.AppMessage{Event: "ready", URL: "http://127.0.0.1:1", PID: os.Getpid()})
	in := bufio.NewScanner(os.Stdin)
	for asked := 0; in.Scan(); asked++ {
		var req cli.AppMessage
		_ = json.Unmarshal(in.Bytes(), &req)
		if mode == "late" && asked == 0 {
			time.Sleep(300 * time.Millisecond)
		}
		_ = enc.Encode(cli.AppMessage{Event: "link", Page: req.Page, URL: "http://127.0.0.1:1/ui/enter?t=fake&next=" + req.Page})
	}
	// Standard input closed: the app quit.
}

// heldLock is the data directory lock the fake linked wasync holds.
var heldLock *processlock.Lock

// holdLinked takes the data directory the way a running wasync does and
// answers on its local API that a phone is linked.
func holdLinked(args []string) error {
	var dir string
	for i, a := range args {
		if a == "--data-dir" && i+1 < len(args) {
			dir = args[i+1]
		}
	}
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	lock, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		return err
	}
	// Kept for the life of the process: a lock nothing refers to is closed by
	// the garbage collector, which lets the data directory go.
	heldLock = lock
	token := filepath.Join(dir, "api.token")
	if err := localfs.EnsureFile(token); err != nil {
		return err
	}
	if err := os.WriteFile(token, []byte("fake-token"), 0o600); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	pid := os.Getpid()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/owner", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"owner":{"pid":%d},"connection":"online","paired":true}`, pid)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"enabled_chats":2}`)
	})
	mux.HandleFunc("/api/ui/link", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Page string `json:"page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fmt.Fprintf(w, `{"url":"http://%s/ui/enter?t=other&next=%s"}`, ln.Addr(), req.Page)
	})
	approve := filepath.Join(dir, approvecred.FileName)
	if err := localfs.EnsureFile(approve); err != nil {
		return err
	}
	if err := os.WriteFile(approve, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		return err
	}
	go func() { _ = http.Serve(ln, mux) }()
	return processlock.WriteOwner(dir, processlock.Owner{Kind: "setup", PID: pid, DataDir: dir, APIAddr: ln.Addr().String(), StartedAt: time.Now()})
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func testHost(t *testing.T, mode string) (*Host, *syncBuffer, *int) {
	t.Helper()
	log := &syncBuffer{}
	starts := 0
	var mu sync.Mutex
	h := &Host{Wasync: "wasync", App: "/Applications/wasync.app/Contents/MacOS/wasync-desktop", Log: log, CLI: &cli.App{Out: io.Discard, Err: io.Discard},
		Config: config.Config{DataDir: filepath.Join(t.TempDir(), "data"), APIAddr: "127.0.0.1:1", Timeout: time.Second},
		Command: func(name string, args ...string) *exec.Cmd {
			mu.Lock()
			starts++
			mu.Unlock()
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "WASYNC_FAKE="+mode)
			return cmd
		},
		ReadyWait: 10 * time.Second}
	t.Cleanup(func() { _ = h.Stop(5 * time.Second) })
	return h, log, &starts
}

func TestHostStartsWasyncOnceAsksItForLinksAndStopsItOnQuit(t *testing.T) {
	h, _, starts := testHost(t, "serve")
	ctx := context.Background()
	link, err := h.Link(ctx, "setup")
	if err != nil || link != "http://127.0.0.1:1/ui/enter?t=fake&next=setup" {
		t.Fatalf("link %q %v", link, err)
	}
	if link, err = h.Link(ctx, "chats"); err != nil || !strings.HasSuffix(link, "next=chats") {
		t.Fatalf("second link %q %v", link, err)
	}
	if *starts != 1 {
		t.Fatalf("wasync started %d times", *starts)
	}
	c := h.child
	if err := h.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if c.alive() || c.err != nil {
		t.Fatalf("closing standard input must stop wasync cleanly: %v", c.err)
	}
	if _, err := h.Ensure(ctx); err != ErrStopped {
		t.Fatalf("after quitting nothing starts again: %v", err)
	}
}

func TestHostReportsAWasyncThatFailsToStart(t *testing.T) {
	h, log, _ := testHost(t, "fail")
	if _, err := h.Link(context.Background(), "setup"); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(log.String(), "archive is unavailable") {
		t.Fatalf("wasync's own words belong in the log: %q", log.String())
	}
	if got := h.Status(context.Background()); got != "wasync stopped after a problem; trying again" {
		t.Fatalf("status %q", got)
	}
}

func TestStartPageIsSetupUntilFirstOpenIsSeenAndAPhoneIsLinked(t *testing.T) {
	h, _, _ := testHost(t, "serve")
	if got := h.StartPage(context.Background()); got != "setup" {
		t.Fatalf("first open: %q", got)
	}
	if err := firstopen.Done(h.Config.DataDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := h.StartPage(context.Background()); got != "setup" {
		t.Fatalf("no wasync answers, so no phone is known to be linked: %q", got)
	}
}

func TestStartLinkOpensTheChatsOnceTheWasyncItStartsSaysAPhoneIsLinked(t *testing.T) {
	h, _, starts := testHost(t, "linked")
	if err := firstopen.Done(h.Config.DataDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	link, err := h.StartLink(context.Background())
	if err != nil || !strings.HasSuffix(link, "next=chats") {
		t.Fatalf("a linked phone opens on the chats: %q %v", link, err)
	}
	if *starts != 1 {
		t.Fatalf("wasync started %d times", *starts)
	}
}

func TestALateLinkAnswerDoesNotReachTheNextRequest(t *testing.T) {
	h, _, _ := testHost(t, "late")
	if _, err := h.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.Link(ctx, "setup"); err == nil {
		t.Fatal("the first request must give up before wasync answers")
	}
	time.Sleep(500 * time.Millisecond)
	for _, page := range []string{"chats", "setup", "chats"} {
		link, err := h.Link(context.Background(), page)
		if err != nil || !strings.HasSuffix(link, "next="+page) {
			t.Fatalf("asked for %s, got %q %v", page, link, err)
		}
	}
}

// holdDataDirectory starts a separate wasync that takes dir and answers on
// its local API, the way `wasync serve` started at login does. stop stops it.
func holdDataDirectory(t *testing.T, dir string) (stop func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "--data-dir", dir)
	cmd.Env = append(os.Environ(), "WASYNC_FAKE=hold")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop = func() { once.Do(func() { _ = stdin.Close(); _ = cmd.Wait() }) }
	t.Cleanup(stop)
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "held\n" {
		t.Fatalf("the other wasync did not take the data directory: %q", line)
	}
	return stop
}

func TestHostUsesAWasyncThatTookTheDataDirectoryWhileItsOwnWasStarting(t *testing.T) {
	h, _, starts := testHost(t, "busy")
	start := h.Command
	var stopOther func()
	h.Command = func(name string, args ...string) *exec.Cmd {
		stopOther = holdDataDirectory(t, h.Config.DataDir)
		return start(name, args...)
	}
	link, err := h.Link(context.Background(), "chats")
	if err != nil || !strings.Contains(link, "t=other&next=chats") {
		t.Fatalf("the window must show the wasync that took the data directory: %q %v", link, err)
	}
	if *starts != 1 {
		t.Fatalf("wasync started %d times", *starts)
	}
	stopOther()
	if got := h.Status(context.Background()); got != "wasync is not running" {
		t.Fatalf("once the other wasync stops, the app's own lost start is no problem to report: %q", got)
	}
}

func TestTheAppQuitsWhenItsWasyncStopsToInstallAnUpdate(t *testing.T) {
	h, _, _ := testHost(t, "serve")
	quit := make(chan struct{}, 1)
	h.OnUpdate = func() { quit <- struct{}{} }
	if _, err := h.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := localfs.EnsureDir(update.Folder(h.Config.DataDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(update.PlanPath(h.Config.DataDir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	c := h.child
	h.mu.Unlock()
	_ = c.stdin.Close() // wasync stops cleanly, as it does for an update
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("the app must quit so the update can open the new app")
	}
}

func TestAnUpdatePlanLeftFromHoursAgoDoesNotQuitTheApp(t *testing.T) {
	h, _, _ := testHost(t, "serve")
	h.OnUpdate = func() { t.Error("a plan no helper finished hours ago is no update in progress") }
	if _, err := h.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := localfs.EnsureDir(update.Folder(h.Config.DataDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(update.PlanPath(h.Config.DataDir), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(update.PlanPath(h.Config.DataDir), old, old); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	c := h.child
	h.mu.Unlock()
	_ = c.stdin.Close()
	<-c.done
}

func TestAWasyncThatStopsCleanlyWithoutAnUpdateLeavesTheAppRunning(t *testing.T) {
	h, _, _ := testHost(t, "serve")
	h.OnUpdate = func() { t.Error("no update is waiting") }
	if _, err := h.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	c := h.child
	h.mu.Unlock()
	_ = c.stdin.Close()
	<-c.done
}

func TestDescribe(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		st   cli.OwnerStatus
		own  bool
		last string
		want string
	}{
		{cli.OwnerStatus{}, true, "", "wasync is not running"},
		{cli.OwnerStatus{}, true, "exit status 1", "wasync stopped after a problem; trying again"},
		{cli.OwnerStatus{Running: true}, true, "", "wasync is starting…"},
		{cli.OwnerStatus{Running: true, Answered: true, Paired: &no, Connection: "pairing"}, true, "", "Not linked to WhatsApp yet"},
		{cli.OwnerStatus{Running: true, Answered: true, Paired: &yes, Connection: "online", SyncedChats: 1}, true, "", "Syncing 1 chat"},
		{cli.OwnerStatus{Running: true, Answered: true, Paired: &yes, Connection: "online", SyncedChats: 12}, false, "", "Syncing 12 chats (in another wasync)"},
		{cli.OwnerStatus{Running: true, Answered: true, Paired: &yes, Connection: "reconnecting"}, true, "", "Connecting to WhatsApp…"},
		{cli.OwnerStatus{Running: true, Answered: true, Paired: &yes, Connection: "attention required"}, true, "", "WhatsApp needs attention: open wasync"},
		{cli.OwnerStatus{Running: true, Answered: true, Connection: "offline"}, true, "", "Offline"},
	} {
		if got := Describe(tc.st, tc.own, tc.last); got != tc.want {
			t.Errorf("Describe(%+v, %v, %q) = %q, want %q", tc.st, tc.own, tc.last, got, tc.want)
		}
	}
}

func TestRestartWaitsLongerAfterEachFailureUpToAMinute(t *testing.T) {
	if restartAfter(1) != 2*time.Second || restartAfter(3) != 8*time.Second || restartAfter(20) != time.Minute {
		t.Fatal(restartAfter(1), restartAfter(3), restartAfter(20))
	}
}
