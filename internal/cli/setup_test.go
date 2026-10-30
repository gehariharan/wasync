package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// linkingClient is a live client that has not been linked yet: it offers one
// pairing code, waits for the test to "scan" it, then reports linked.
type linkingClient struct {
	boundedSyncClient
	mu      sync.Mutex
	linked  bool
	scanned chan struct{}
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
	display("2@TESTPAIRINGCODE,key,key")
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
	client := &linkingClient{scanned: make(chan struct{})}
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
	page := waitFor("<svg")
	if strings.Contains(page, "TESTPAIRINGCODE") {
		t.Fatal("the pairing code appeared as text")
	}
	close(client.scanned)
	waitFor("Linked.")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
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
