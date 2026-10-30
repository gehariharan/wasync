package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/gehariharan/wasync/internal/storage"
)

type loginItem struct {
	mu        sync.Mutex
	installed bool
	spec      service.Spec
}

func (l *loginItem) Where() string { return "a test login item" }
func (l *loginItem) Install(s service.Spec) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.installed, l.spec = true, s
	return nil
}
func (l *loginItem) Uninstall() error { l.mu.Lock(); l.installed = false; l.mu.Unlock(); return nil }
func (l *loginItem) Installed() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.installed, nil
}

func TestServiceInstallIsAHumanChoice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	item := &loginItem{}
	stub := &humanStub{}
	build := func(input string, interactive bool) (*App, *bytes.Buffer) {
		var out bytes.Buffer
		return &App{In: &lineReader{[]string{input}}, Out: &out, Err: &bytes.Buffer{}, Interactive: interactive, Service: item, Human: humancheck.NewGate(stub)}, &out
	}
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "service"}, args...))
	}

	app, _ := build("INSTALL", false)
	if err := run(app, "install"); err == nil || !strings.Contains(err.Error(), "interactive terminal") || item.installed {
		t.Fatalf("non-interactive install: %v", err)
	}
	app, _ = build("yes", true)
	if err := run(app, "install"); err == nil || item.installed {
		t.Fatalf("weak confirmation: %v", err)
	}
	app, _ = build("INSTALL", true)
	if err := run(app, "install"); err == nil || !strings.Contains(err.Error(), "nothing installed") || item.installed {
		t.Fatalf("typed word but the person declined: %v", err)
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], "every time you log in") {
		t.Fatalf("prompt: %q", stub.asked)
	}
	stub.confirm = true
	app, out := build("INSTALL", true)
	if err := run(app, "install"); err != nil || !item.installed {
		t.Fatalf("confirmed install: %v", err)
	}
	if item.spec.DataDir != dir || !filepath.IsAbs(item.spec.Exe) || !strings.Contains(out.String(), "next time you log in") {
		t.Fatalf("spec %+v out %q", item.spec, out.String())
	}
	app, out = build("", false)
	if err := run(app, "status"); err != nil || !strings.Contains(out.String(), "installed") || strings.Contains(out.String(), "not installed") {
		t.Fatalf("status: %v %q", err, out.String())
	}
	app, _ = build("", false)
	if err := run(app, "uninstall"); err != nil || item.installed {
		t.Fatalf("uninstall needs no terminal or prompt: %v", err)
	}
	app, _ = build("", false)
	if err := run(app, "bogus"); err == nil {
		t.Fatal("accepted an unknown action")
	}
	app = &App{Out: io.Discard, Err: io.Discard}
	if err := run(app, "install"); !errors.Is(err, service.ErrUnsupported) {
		t.Fatalf("platform without a login item: %v", err)
	}
}

func TestServiceInstallKeepsThePathThatSurvivesAnUpgrade(t *testing.T) {
	root := t.TempDir()
	versioned := filepath.Join(root, "Cellar", "wasync", "1.0.0", "bin", "wasync")
	link := filepath.Join(root, "bin", "wasync")
	for _, d := range []string{filepath.Dir(versioned), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(versioned, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versioned, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	item := &loginItem{}
	app := &App{In: &lineReader{[]string{"INSTALL"}}, Out: io.Discard, Err: io.Discard, Interactive: true, Service: item,
		Human: humancheck.NewGate(&humanStub{confirm: true}), Executable: func() (string, error) { return link, nil }}
	if err := app.Run(context.Background(), []string{"--data-dir", filepath.Join(root, "data"), "service", "install"}); err != nil {
		t.Fatal(err)
	}
	if item.spec.Exe != link {
		t.Fatalf("login item starts %q, want the link %q that upgrades keep", item.spec.Exe, link)
	}
}

// refusingKeychain answers every read with err, as the macOS Keychain does
// when the person cancels or denies its prompt.
type refusingKeychain struct {
	err  error
	sets int
}

func (k *refusingKeychain) Get(string) (string, error) { return "", k.err }
func (k *refusingKeychain) Set(string, string) error   { k.sets++; return nil }
func (k *refusingKeychain) Delete(string) error        { return nil }

func TestServeStopsCleanlyWhenTheKeychainIsRefused(t *testing.T) {
	keychain := &refusingKeychain{err: approvecred.ErrAccessDenied}
	approvecred.Use(keychain)
	t.Cleanup(func() { approvecred.Use(nil) })
	client := &boundedSyncClient{}
	var notes []string
	app := &App{Out: io.Discard, Err: io.Discard, Human: humancheck.NewGate(&humanStub{}),
		Notify:     func(_, text string) { notes = append(notes, text) },
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil }}
	serve := func() error {
		return app.Run(context.Background(), []string{"--data-dir", filepath.Join(t.TempDir(), "data"), "--api-addr", "127.0.0.1:" + freePort(t), "serve"})
	}
	if err := serve(); err != nil {
		t.Fatalf("a refused Keychain must exit successfully so the login item does not restart it: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Approvals are off") || !strings.Contains(notes[0], "Run wasync again") {
		t.Fatalf("notifications %q", notes)
	}
	if client.connect {
		t.Fatal("the owner started without its approval credential")
	}
	if keychain.sets != 0 {
		t.Fatal("a refused read created a new credential")
	}

	keychain.err, notes = errors.New("keychain read failed (-1)"), nil
	if err := serve(); err == nil || len(notes) != 0 {
		t.Fatalf("any other Keychain failure must still fail: %v %q", err, notes)
	}
}

// linkingClient is a live client that has not been linked yet: it offers one
// pairing code, waits for the test to "scan" it, then reports linked.
type linkingClient struct {
	boundedSyncClient
	mu       sync.Mutex
	linked   bool
	scanned  chan struct{}
	timeouts int // attempts that end unscanned before one waits for the scan
	attempts int
}

// Before the scan there is no account yet, as with a real unlinked client.
func (c *linkingClient) AccountJID() string {
	if !c.Paired() {
		return ""
	}
	return c.boundedSyncClient.AccountJID()
}
func (c *linkingClient) EnableCapture(ctx context.Context, s *storage.Store) error {
	if !c.Paired() {
		return nil
	}
	return c.boundedSyncClient.EnableCapture(ctx, s)
}
func (c *linkingClient) Paired() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.linked }
func (c *linkingClient) LoginWithQR(ctx context.Context, display func(string)) error {
	c.mu.Lock()
	c.attempts++
	expired := c.attempts <= c.timeouts
	c.mu.Unlock()
	display("2@TESTPAIRINGCODE,key,key")
	if expired {
		return errors.New("QR pairing stopped (timeout); check the phone and run login again")
	}
	select {
	case <-c.scanned:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	c.linked, c.connect = true, true
	c.mu.Unlock()
	return nil
}

func TestSetupLinksWhatsAppFromThePageAndOpensItByItself(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	client := &linkingClient{scanned: make(chan struct{}), timeouts: 1}
	opened := make(chan string, 4)
	var out bytes.Buffer
	app := &App{Out: &lockedWriter{w: &out}, Err: io.Discard, Human: humancheck.NewGate(&humanStub{}),
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil },
		OpenURL:    func(u string) error { opened <- u; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	api := "127.0.0.1:" + freePort(t)
	go func() { done <- app.Run(ctx, []string{"--data-dir", dir, "--api-addr", api, "setup"}) }()

	var link string
	select {
	case link = <-opened:
	case err := <-done:
		t.Fatalf("setup exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("setup did not open the page")
	}
	if !strings.Contains(link, "/ui/enter?t=") || !strings.Contains(link, "next=setup") {
		t.Fatalf("link %q", link)
	}
	if strings.Contains(out.String(), "t=") {
		t.Fatalf("the one-time link was printed: %q", out.String())
	}
	jar := &simpleJarCLI{}
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u string) (int, string) {
		res, err := browser.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, _ := get(link); code != 303 {
		t.Fatalf("enter: %d", code)
	}
	base := "http://" + api
	waitFor := func(want string) string {
		t.Helper()
		var page string
		for i := 0; i < 100; i++ {
			_, page = get(base + "/ui/setup")
			if strings.Contains(page, want) {
				return page
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("page never showed %q:\n%.500s", want, page)
		return ""
	}
	// The first code expires unscanned: the page stays up, says so, and offers a new code.
	page := waitFor("Get a new code")
	if !strings.Contains(page, "timeout") {
		t.Fatalf("failure not shown:\n%.500s", page)
	}
	i := strings.Index(page, `name="csrf" value="`) + len(`name="csrf" value="`)
	csrf := page[i : i+strings.Index(page[i:], `"`)]
	for range 2 { // a second click must not start a second attempt
		res, err := browser.PostForm(base+"/ui/setup/link", url.Values{"csrf": {csrf}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 303 {
			t.Fatalf("new code: %d", res.StatusCode)
		}
	}
	page = waitFor("<svg")
	if strings.Contains(page, "TESTPAIRINGCODE") {
		t.Fatal("the pairing code appeared as text")
	}
	close(client.scanned)
	waitFor("Linked.")
	waitFor(`value="456@g.us"`) // the chat list loaded after linking
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.attempts != 2 {
		t.Fatalf("linking attempts %d, want 2", client.attempts)
	}
}

// stallingClient stops at one step of setup, says it got there, and waits
// until setup is stopped.
type stallingClient struct {
	*linkingClient
	inDiscovery bool
	reached     chan struct{}
}

func (c *stallingClient) LoginWithQR(ctx context.Context, display func(string)) error {
	if c.inDiscovery {
		return c.linkingClient.LoginWithQR(ctx, display)
	}
	close(c.reached)
	<-ctx.Done()
	return ctx.Err()
}
func (c *stallingClient) Groups(ctx context.Context) ([]storage.Group, error) {
	close(c.reached)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSetupStoppedWhileLinkingOrLoadingChatsExitsCleanly(t *testing.T) {
	for _, inDiscovery := range []bool{false, true} {
		scanned := make(chan struct{})
		if inDiscovery {
			close(scanned) // the phone links at once, so setup goes on to load the chat list
		}
		client := &stallingClient{linkingClient: &linkingClient{scanned: scanned}, inDiscovery: inDiscovery, reached: make(chan struct{})}
		app := &App{Out: io.Discard, Err: io.Discard, Human: humancheck.NewGate(&humanStub{}),
			OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil },
			OpenURL:    func(string) error { return nil }}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- app.Run(ctx, []string{"--data-dir", filepath.Join(t.TempDir(), "data"), "--api-addr", "127.0.0.1:" + freePort(t), "setup"})
		}()
		select {
		case <-client.reached:
		case err := <-done:
			t.Fatalf("discovery %t: setup exited early: %v", inDiscovery, err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("discovery %t: stopping setup must be a clean exit: %v", inDiscovery, err)
		}
	}
}

func TestSetupStillFailsWhenLoadingChatsFails(t *testing.T) {
	scanned := make(chan struct{})
	close(scanned)
	client := &linkingClient{scanned: scanned}
	client.groupErr = errors.New("group list unavailable")
	app := &App{Out: io.Discard, Err: io.Discard, Human: humancheck.NewGate(&humanStub{}),
		OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil },
		OpenURL:    func(string) error { return nil }}
	err := app.Run(context.Background(), []string{"--data-dir", filepath.Join(t.TempDir(), "data"), "--api-addr", "127.0.0.1:" + freePort(t), "setup"})
	if err == nil || !strings.Contains(err.Error(), "group list unavailable") {
		t.Fatalf("a real failure must still be reported: %v", err)
	}
}

func TestSetupWhenWasyncIsAlreadyRunningOpensItsSetupPage(t *testing.T) {
	app, _, run, _, _, _, _ := ownerFixture(t)
	var opened string
	app.OpenURL = func(u string) error { opened = u; return nil }
	app.Err = &bytes.Buffer{}
	if err := run("setup"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opened, "next=setup") {
		t.Fatalf("opened %q", opened)
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type simpleJarCLI struct{ cookies []*http.Cookie }

func (j *simpleJarCLI) SetCookies(_ *url.URL, c []*http.Cookie) { j.cookies = append(j.cookies, c...) }
func (j *simpleJarCLI) Cookies(*url.URL) []*http.Cookie         { return j.cookies }
