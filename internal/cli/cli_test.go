package cli

import (
	"bytes"
	"context"
	"errors"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeClient struct {
	groupErr  error
	logoutErr error
	logout    bool

	closed  bool
	login   bool
	connect bool
}

func (f *fakeClient) Close() error                           { f.closed = true; return nil }
func (f *fakeClient) Paired() bool                           { return true }
func (f *fakeClient) AccountJID() string                     { return "test@s.whatsapp.net" }
func (f *fakeClient) Connect(context.Context) error          { f.connect = true; return nil }
func (f *fakeClient) Login(context.Context, io.Writer) error { f.login = true; return nil }
func (f *fakeClient) Logout(context.Context) error           { f.logout = true; return f.logoutErr }
func (f *fakeClient) Groups(context.Context) ([]storage.Group, error) {
	return []storage.Group{{ID: "123@g.us", Name: "Selected"}, {ID: "456@g.us", Name: "Other"}}, f.groupErr
}
func (f *fakeClient) Observe(context.Context, time.Duration) error { return nil }
func (f *fakeClient) Observation() whatsapp.Observation {
	return whatsapp.Observation{HistoryBatches: 1}
}
func TestLoginSelectionAndOfflineCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var out, logs bytes.Buffer
	f := &fakeClient{}
	app := &App{In: strings.NewReader("2,2\n"), Out: &out, Err: &logs, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return f, nil }}
	ctx := context.Background()
	if err := app.Run(ctx, []string{"--data-dir", dir, "login", "--select=true"}); err != nil {
		t.Fatal(err)
	}
	if !f.login || !f.closed {
		t.Fatal("login/close lifecycle not invoked")
	}
	// Offline list and preference changes must not open a WhatsApp client.
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("offline command opened client")
		return nil, nil
	}
	out.Reset()
	if err := app.Run(ctx, []string{"--data-dir", dir, "groups", "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"Enabled":true`) {
		t.Fatal(out.String())
	}
	if err := app.Run(ctx, []string{"--data-dir", dir, "groups", "disable", "123@g.us"}); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.Account(ctx)
	groups, _ := s.Groups(ctx, a.ID)
	for _, g := range groups {
		if g.Enabled {
			t.Fatal("disable preference not persisted")
		}
	}
}
func TestSelectionRejectsInvalidInput(t *testing.T) {
	groups := []storage.Group{{ID: "123@g.us"}, {ID: "456@g.us"}}
	for _, input := range []string{"0", "3", "1,garbage", "1,", "-1"} {
		if _, err := parseSelection(input, groups); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	ids, err := parseSelection("2,1,2", groups)
	if err != nil || len(ids) != 2 {
		t.Fatalf("dedup selection failed %v %v", ids, err)
	}
}
func TestDiscoveryFailureStillClosesClient(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var buf bytes.Buffer
	f := &fakeClient{groupErr: errors.New("discovery unavailable")}
	a := &App{In: strings.NewReader(""), Out: &buf, Err: &buf, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return f, nil }}
	err := a.Run(context.Background(), []string{"--data-dir", dir, "login"})
	if err == nil || !strings.Contains(err.Error(), "discovery failed") {
		t.Fatalf("expected actionable discovery failure, got %v", err)
	}
	if !f.closed {
		t.Fatal("client was not closed after discovery failed")
	}
}
func TestFailedLogoutDoesNotClaimSuccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var buf bytes.Buffer
	f := &fakeClient{logoutErr: errors.New("unlink unavailable")}
	a := &App{In: strings.NewReader(""), Out: &buf, Err: &buf, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return f, nil }}
	err := a.Run(context.Background(), []string{"--data-dir", dir, "logout"})
	if err == nil || !f.connect || !f.logout || !f.closed {
		t.Fatalf("logout lifecycle: %+v %v", f, err)
	}
	if strings.Contains(buf.String(), "Device unlinked") {
		t.Fatal("failed remote unlink claimed success")
	}
}

type channelOutput struct{ writes chan string }

func (w channelOutput) Write(p []byte) (int, error) { w.writes <- string(p); return len(p), nil }

func TestHeadlessOfflineLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	output := channelOutput{make(chan string, 10)}
	app := &App{Out: output, Err: io.Discard, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) {
		t.Error("offline server opened WhatsApp")
		return nil, errors.New("unexpected client")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, []string{"--data-dir", dir, "--timeout", "1ns", "--api-addr", "127.0.0.1:0", "serve", "--offline"})
	}()
	select {
	case text := <-output.writes:
		if !strings.Contains(text, "Local API: http://127.0.0.1:") || !strings.Contains(text, "api.token") {
			t.Fatalf("missing API instructions: %s", text)
		}
	case err := <-done:
		t.Fatalf("server exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("serve used diagnostic timeout: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if _, err := os.Stat(filepath.Join(dir, "session.db")); !os.IsNotExist(err) {
		t.Fatal("offline serve created credentials")
	}
	// A second command can acquire the lock and open the archive after shutdown.
	app.Out = io.Discard
	if err := app.Run(context.Background(), []string{"--data-dir", dir, "status"}); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveLoginOpensTUIInsteadOfGroupTable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var out, logs bytes.Buffer
	f := &fakeClient{}
	opened := 0
	app := &App{In: strings.NewReader(""), Out: &out, Err: &logs, Interactive: true,
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return f, nil },
		OpenTUI: func(context.Context, config.Config, *storage.Store, *slog.Logger) error {
			if !f.closed {
				t.Error("login connection still open when the TUI started")
			}
			opened++
			return nil
		}}
	if err := app.Run(context.Background(), []string{"--data-dir", dir, "login"}); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || !f.login {
		t.Fatalf("login did not continue into the TUI: opened=%d login=%t", opened, f.login)
	}
	if strings.Contains(out.String(), "GROUP ID") || strings.Contains(out.String(), "Selected") {
		t.Fatalf("group table printed on interactive login:\n%s", out.String())
	}
	if logs.Len() != 0 {
		t.Fatalf("raw log lines reached the terminal: %q", logs.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "wasync.log")); err != nil {
		t.Fatalf("login logs not written to wasync.log: %v", err)
	}
}

func TestLoginKeepsDiagnosticOutputWhenNotOpeningTUI(t *testing.T) {
	for name, tc := range map[string]struct {
		interactive bool
		args        []string
	}{
		"not a terminal":    {false, []string{"login"}},
		"select disabled":   {true, []string{"login", "--select=false"}},
		"observe requested": {true, []string{"login", "--observe", "1ms"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			var out bytes.Buffer
			app := &App{In: strings.NewReader(""), Out: &out, Err: io.Discard, Interactive: tc.interactive,
				OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return &fakeClient{}, nil },
				OpenTUI: func(context.Context, config.Config, *storage.Store, *slog.Logger) error {
					t.Error("TUI opened")
					return nil
				}}
			if err := app.Run(context.Background(), append([]string{"--data-dir", dir}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Selected") {
				t.Fatalf("group table missing:\n%s", out.String())
			}
		})
	}
}
