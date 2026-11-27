package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/processlock"
)

// The wasync app starts `wasync setup --app`, asks it for one-time links on
// standard input and reads them on standard output, and stops it by closing
// standard input. No browser opens and no link reaches a log.
func TestSetupForTheAppAnswersOnItsPipesAndStopsWhenTheAppGoes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	desktop := filepath.Join(t.TempDir(), "wasync.app", "Contents", "MacOS", "wasync-desktop")
	client := &linkingClient{scanned: make(chan struct{})}
	close(client.scanned) // already linked
	toWasync, fromApp := io.Pipe()
	fromWasync, toApp := io.Pipe()
	var said bytes.Buffer
	item := &loginItem{}
	serveItem := &loginItem{}
	human := &humanStub{confirm: true}
	app := &App{In: toWasync, Out: toApp, Err: &lockedWriter{w: &said}, Human: humancheck.NewGate(human),
		Service: serveItem, AppService: item,
		Getenv:     func(k string) string { return map[string]string{DesktopAppEnv: desktop}[k] },
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil },
		OpenURL:    func(u string) error { t.Errorf("a browser was opened for the app: %s", u); return nil }}
	api := "127.0.0.1:" + freePort(t)
	done := make(chan error, 1)
	go func() {
		done <- app.Run(context.Background(), []string{"--data-dir", dir, "--api-addr", api, "setup", "--app"})
		toApp.Close()
	}()
	replies := bufio.NewScanner(fromWasync)
	next := func() AppMessage {
		t.Helper()
		got := make(chan AppMessage, 1)
		go func() {
			var m AppMessage
			if replies.Scan() {
				if err := json.Unmarshal(replies.Bytes(), &m); err != nil {
					m.Error = "not JSON: " + replies.Text()
				}
			}
			got <- m
		}()
		select {
		case m := <-got:
			return m
		case err := <-done:
			t.Fatalf("setup --app exited: %v\n%s", err, said.String())
		case <-time.After(10 * time.Second):
			t.Fatal("no answer from setup --app")
		}
		return AppMessage{}
	}
	ready := next()
	if ready.Event != "ready" || ready.URL != "http://"+api || ready.PID == 0 {
		t.Fatalf("first line %+v", ready)
	}
	ask := func(req string) AppMessage {
		t.Helper()
		if _, err := io.WriteString(fromApp, req+"\n"); err != nil {
			t.Fatal(err)
		}
		return next()
	}
	if m := ask(`{"op":"link","page":"elsewhere"}`); m.Event != "error" || m.URL != "" {
		t.Fatalf("a page outside the list: %+v", m)
	}
	if m := ask(`not json`); m.Event != "error" {
		t.Fatalf("garbage: %+v", m)
	}
	link := ask(`{"op":"link","page":"setup"}`)
	if link.Event != "link" || !strings.Contains(link.URL, "/ui/enter?t=") || !strings.Contains(link.URL, "next=setup") {
		t.Fatalf("link %+v", link)
	}
	if strings.Contains(said.String(), "t=") || !strings.Contains(said.String(), "Local API") {
		t.Fatalf("what wasync says for a person belongs on standard error, without the link:\n%s", said.String())
	}
	owner, ok := processlock.ReadOwner(dir)
	if !ok || owner.StartedBy != startedByApp {
		t.Fatalf("owner record %+v %v", owner, ok)
	}

	// The app's status line and, for a wasync it did not start, its links.
	cfg := config.Config{DataDir: dir, APIAddr: "127.0.0.1:1", Timeout: 10 * time.Second}
	st := app.Status(context.Background(), cfg)
	if !st.Running || !st.Answered || st.PID != ready.PID || st.Paired == nil || !*st.Paired || st.Kind != "setup" {
		t.Fatalf("status %+v", st)
	}
	if other, err := app.PageLink(context.Background(), cfg, "chats"); err != nil || !strings.Contains(other, "next=chats") {
		t.Fatalf("link through the owner's own address: %q %v", other, err)
	}
	if _, err := app.PageLink(context.Background(), cfg, "../approvals"); err == nil {
		t.Fatal("a page outside the list was accepted")
	}
	if nobody := app.Status(context.Background(), config.Config{DataDir: t.TempDir(), APIAddr: api, Timeout: time.Second}); nobody.Running || nobody.Answered {
		t.Fatalf("nothing runs there: %+v", nobody)
	}

	// The setup page's start-at-login switch opens the app at login, not wasync serve.
	jar := &simpleJarCLI{}
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := browser.Get(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res, err = browser.Get("http://" + api + "/ui/settings")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	i := bytes.Index(page, []byte(`name="csrf" value="`)) + len(`name="csrf" value="`)
	csrf := string(page[i : i+bytes.IndexByte(page[i:], '"')])
	res, err = browser.PostForm("http://"+api+"/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"install"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !item.installed || item.spec.Exe != desktop || item.spec.DataDir != dir || serveItem.installed {
		t.Fatalf("app item %+v, serve item installed %v", item, serveItem.installed)
	}
	if item.starts() != 0 {
		t.Fatal("turning on start-at-login must not start a second wasync: the app already runs one")
	}

	fromApp.Close() // the app quit
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopping because the app went away is a clean exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("setup --app kept running after the app went away")
	}
	if held, _ := processlock.Held(filepath.Join(dir, "device.lock")); held {
		t.Fatal("the data directory is still held")
	}
}

func TestSetupTakesOnlyTheAppFlag(t *testing.T) {
	app := &App{Out: io.Discard, Err: io.Discard}
	err := app.Run(context.Background(), []string{"--data-dir", filepath.Join(t.TempDir(), "data"), "setup", "extra"})
	if err == nil || !strings.Contains(err.Error(), "usage: wasync setup [--app]") {
		t.Fatalf("got %v", err)
	}
}
