package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
)

func TestCommandsThatCannotShareNameTheOwnerAndHowToStopIt(t *testing.T) {
	owner := startServeOwner(t)
	app, _, _ := otherApp(t)
	app.Interactive = true // so "run" gets as far as the data directory
	pid := fmt.Sprintf("PID %d", ownerPID(t, owner))
	for _, command := range []string{"login", "serve", "run", "tui"} {
		err := app.Run(context.Background(), []string{"--data-dir", owner.dir, command})
		if err == nil {
			t.Fatalf("%s ran while serve owned the directory", command)
		}
		for _, want := range []string{"wasync serve", pid, owner.dir, owner.addr, " stop"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s refusal does not mention %q: %v", command, want, err)
			}
		}
		if strings.Contains(err.Error(), "quit it first") {
			t.Errorf("%s still gives the bare refusal: %v", command, err)
		}
		if !errors.Is(err, processlock.ErrBusy) {
			t.Errorf("%s refusal no longer matches ErrBusy: %v", command, err)
		}
	}
}

func ownerPID(t *testing.T, o *runningOwner) int {
	t.Helper()
	record, ok := processlock.ReadOwner(o.dir)
	if !ok {
		t.Fatal("the running serve left no owner record")
	}
	return record.PID
}

func TestStopEndsAServeOwnerWithoutKillingAnything(t *testing.T) {
	owner := startServeOwner(t)
	app, out, _ := otherApp(t)
	if err := app.Run(context.Background(), []string{"--data-dir", owner.dir, "stop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wasync serve", "PID", owner.dir, "Stopped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stop output is missing %q:\n%s", want, out.String())
		}
	}
	owner.wait(t, 15*time.Second)
	if owner.held() {
		t.Fatal("the data directory is still locked after stop")
	}
	if _, ok := processlock.ReadOwner(owner.dir); ok {
		t.Fatal("a stopped owner left its record behind")
	}
	if owner.client.loggedOut.Load() {
		t.Fatal("stop unlinked the device")
	}
	// Stopping again, with nothing running, is not an error.
	out.mu.Lock()
	out.buf.Reset()
	out.mu.Unlock()
	if err := app.Run(context.Background(), []string{"--data-dir", owner.dir, "stop"}); err != nil || !strings.Contains(out.String(), "No wasync is running") {
		t.Fatalf("stop with nothing running: %v %q", err, out.String())
	}
}

func TestLogoutWithNothingLinkedLeavesTheOwnerRunning(t *testing.T) {
	owner := startServeOwner(t)
	owner.client.paired.Store(false)
	app, out, _ := otherApp(t)
	if err := app.Run(context.Background(), []string{"--data-dir", owner.dir, "logout"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No saved session to unlink") || !owner.held() || owner.client.loggedOut.Load() {
		t.Fatalf("logout with nothing linked: held=%v\n%s", owner.held(), out.String())
	}
}

func TestOwnerWithoutARecordIsNamedAsUnknownNotBare(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	// An older wasync, or a command that has not yet said who it is.
	lock, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	app, _, _ := otherApp(t)
	for _, command := range []string{"status", "logout", "login", "stop"} {
		err := app.Run(context.Background(), []string{"--data-dir", dir, command})
		if err == nil || !strings.Contains(err.Error(), "did not say which") || !strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "quit it first") {
			t.Errorf("%s: %v", command, err)
		}
	}
}

func TestStaleOwnerRecordIsIgnored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	// A crashed owner left its record, but nothing holds the lock any more.
	if err := processlock.WriteOwner(dir, processlock.Owner{Kind: "serve", PID: 2147480000, DataDir: dir, APIAddr: "127.0.0.1:1", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	app, out, _ := otherApp(t)
	if err := app.Run(context.Background(), []string{"--data-dir", dir, "stop"}); err != nil || !strings.Contains(out.String(), "No wasync is running") {
		t.Fatalf("stale record: %v %q", err, out.String())
	}
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) { return &fakeClient{}, nil }
	if err := app.Run(context.Background(), []string{"--data-dir", dir, "logout"}); err != nil {
		t.Fatalf("a stale record blocked a command that could run: %v", err)
	}
}

// An owner with no command channel can still be stopped: the command asks the
// process to shut down, as Ctrl-C would, instead of the person using kill.
func TestStopFallsBackToAskingTheProcessWhenThereIsNoAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot ask a process to stop cleanly")
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=TestHelperHoldsTheDataDirectory")
	helper.Env = append(os.Environ(), "WASYNC_HELPER_DIR="+dir)
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- helper.Wait() }()
	t.Cleanup(func() { _ = helper.Process.Kill() })
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); !strings.Contains(line, "ready") {
		t.Fatalf("helper did not take the directory: %q", line)
	}
	app, out, _ := otherApp(t)
	if err = app.Run(context.Background(), []string{"--data-dir", dir, "stop"}); err != nil {
		t.Fatalf("stop: %v\n%s", err, out.String())
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the helper owner never exited")
	}
	if !strings.Contains(out.String(), "Stopped") {
		t.Fatalf("output: %s", out.String())
	}
}

// TestHelperHoldsTheDataDirectory is not a test: it is the other process the
// test above stops. It holds the directory as `wasync run` without an API.
func TestHelperHoldsTheDataDirectory(t *testing.T) {
	dir := os.Getenv("WASYNC_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	lock, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = processlock.WriteOwner(dir, processlock.Owner{Kind: "run", PID: os.Getpid(), DataDir: dir, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
}

// Stopping is approved with the approval credential. When it cannot be read
// (missing here, as when the Keychain prompt is cancelled), stop reports that
// and leaves the owner running instead of signalling its process.
func TestStopWithoutTheApprovalCredentialLeavesTheOwnerRunning(t *testing.T) {
	owner := startServeOwner(t)
	if err := os.Remove(filepath.Join(owner.dir, "approve.token")); err != nil {
		t.Fatal(err)
	}
	app, out, _ := otherApp(t)
	err := app.Run(context.Background(), []string{"--data-dir", owner.dir, "stop"})
	if err == nil {
		t.Fatalf("stop succeeded without the approval credential:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Stopped") {
		t.Fatalf("stop claimed success:\n%s", out.String())
	}
	time.Sleep(300 * time.Millisecond)
	if !owner.held() {
		t.Fatal("the owner was stopped without approval")
	}
}

// holdAsOwner makes this test process the owner of a fresh data directory,
// with an approval credential file and the given API address on record.
func holdAsOwner(t *testing.T, apiAddr string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "approve.token"), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := processlock.Acquire(filepath.Join(dir, "device.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	if err = processlock.WriteOwner(dir, processlock.Owner{Kind: "serve", PID: os.Getpid(), DataDir: dir, APIAddr: apiAddr, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Any reply from the owner's API means it was reachable, so a refusal is never
// answered by signalling its process; the error says how to stop it instead.
func TestStopReportsAnAPIRefusalInsteadOfSignallingTheOwner(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusConflict, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"refused for the test"}`))
		}))
		dir := holdAsOwner(t, strings.TrimPrefix(server.URL, "http://"))
		app, out, _ := otherApp(t)
		err := app.Run(context.Background(), []string{"--data-dir", dir, "stop"})
		server.Close()
		pid := fmt.Sprintf("%d", os.Getpid())
		if err == nil || !strings.Contains(err.Error(), "refused for the test") || !strings.Contains(err.Error(), "PID "+pid) || !strings.Contains(err.Error(), "end process "+pid) {
			t.Fatalf("status %d: error does not name the PID and how to stop it: %v\n%s", status, err, out.String())
		}
		if strings.Contains(err.Error(), "will not signal") || strings.Contains(out.String(), "Stopped") {
			t.Fatalf("status %d: stop went on to the signal fallback: %v", status, err)
		}
	}
}

// An owner whose API is not answering at all is still stopped by signal; this
// process is the owner here, so reaching the fallback shows as its refusal to signal itself.
func TestStopStillFallsBackWhenTheAPIIsUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()
	dir := holdAsOwner(t, addr)
	app, out, _ := otherApp(t)
	err := app.Run(context.Background(), []string{"--data-dir", dir, "stop"})
	if err == nil || !strings.Contains(err.Error(), "will not signal its own process") {
		t.Fatalf("an unreachable API did not reach the signal fallback: %v\n%s", err, out.String())
	}
}
