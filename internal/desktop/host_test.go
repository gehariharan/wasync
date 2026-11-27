package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/cli"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/firstopen"
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
	_ = enc.Encode(cli.AppMessage{Event: "ready", URL: "http://127.0.0.1:1", PID: os.Getpid()})
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var req cli.AppMessage
		_ = json.Unmarshal(in.Bytes(), &req)
		_ = enc.Encode(cli.AppMessage{Event: "link", Page: req.Page, URL: "http://127.0.0.1:1/ui/enter?t=fake&next=" + req.Page})
	}
	// Standard input closed: the app quit.
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
