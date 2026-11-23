package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// lockedBuffer lets a background owner print while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serveClient is a WhatsApp client for a running owner. Unlike the other fakes
// it is safe to use from the owner's goroutines and the test at once.
type serveClient struct {
	paired, online, loggedOut, closed atomic.Bool
}

func newServeClient() *serveClient {
	c := &serveClient{}
	c.paired.Store(true)
	return c
}
func (c *serveClient) Close() error                           { c.closed.Store(true); return nil }
func (c *serveClient) Paired() bool                           { return c.paired.Load() }
func (c *serveClient) AccountJID() string                     { return "test@s.whatsapp.net" }
func (c *serveClient) Connect(context.Context) error          { c.online.Store(true); return nil }
func (c *serveClient) Login(context.Context, io.Writer) error { return nil }
func (c *serveClient) Logout(context.Context) error {
	c.loggedOut.Store(true)
	c.paired.Store(false)
	return nil
}
func (c *serveClient) Groups(context.Context) ([]storage.Group, error) {
	return []storage.Group{{ID: "123@g.us", Name: "Selected"}}, nil
}
func (c *serveClient) Observe(context.Context, time.Duration) error { return nil }
func (c *serveClient) Observation() whatsapp.Observation            { return whatsapp.Observation{} }
func (c *serveClient) EnableCapture(context.Context, *storage.Store) error {
	return nil
}
func (c *serveClient) LoginWithQR(context.Context, func(string)) error { return nil }
func (c *serveClient) Status() whatsapp.CaptureStatus {
	state := "offline"
	if c.online.Load() {
		state = "online"
	}
	return whatsapp.CaptureStatus{Connection: state}
}
func (c *serveClient) Maintain(context.Context) error { return nil }
func (c *serveClient) StopCapture(error)              {}

// runningOwner is a real `wasync serve` running inside the test process on a
// throwaway data directory. Nothing here touches WhatsApp or the Keychain.
type runningOwner struct {
	dir, addr string
	client    *serveClient
	cancel    context.CancelFunc
	done      chan error
	out       *lockedBuffer
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startServeOwner(t *testing.T, extra ...string) *runningOwner {
	t.Helper()
	o := &runningOwner{dir: filepath.Join(t.TempDir(), "private"), addr: freeAddr(t), client: newServeClient(), done: make(chan error, 1), out: &lockedBuffer{}}
	app := &App{Out: o.out, Err: o.out, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return o.client, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	args := append([]string{"--data-dir", o.dir, "--api-addr", o.addr}, extra...)
	args = append(args, "serve")
	go func() { o.done <- app.Run(ctx, args) }()
	t.Cleanup(func() { cancel(); o.wait(t, 10*time.Second) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		// Ready means a command can find it: the API answers and the owner has
		// recorded where (the record follows the API by a moment).
		if response, err := http.Get("http://" + o.addr + "/health"); err == nil {
			response.Body.Close()
			if record, ok := processlock.ReadOwner(o.dir); ok && record.APIAddr == o.addr {
				break
			}
		}
		select {
		case err := <-o.done:
			o.done = nil
			t.Fatalf("serve stopped before it was ready: %v\n%s", err, o.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never became ready\n%s", o.out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return o
}

// wait returns once the owner has exited, or fails after d.
func (o *runningOwner) wait(t *testing.T, d time.Duration) {
	t.Helper()
	if o.done == nil {
		return
	}
	select {
	case err := <-o.done:
		o.done = nil
		if err != nil {
			t.Errorf("owner exited with an error: %v", err)
		}
	case <-time.After(d):
		t.Fatalf("owner did not exit within %s\n%s", d, o.out.String())
	}
}

func (o *runningOwner) held() bool {
	held, _ := processlock.Held(filepath.Join(o.dir, "device.lock"))
	return held
}

// otherApp is a second wasync command, run from somewhere unrelated.
func otherApp(t *testing.T) (*App, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	t.Chdir(t.TempDir())
	out, errs := &lockedBuffer{}, &lockedBuffer{}
	return &App{Out: out, Err: errs, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("a command that should reach the running owner opened its own WhatsApp client")
		return nil, nil
	}}, out, errs
}

// The report: `wasync status` and `wasync logout`, run from an unrelated
// folder while a headless `wasync serve` owns the data directory, used to stop
// at "another wasync command owns this data directory; manage groups in the
// running TUI or quit it first" without saying who the owner was.
func TestStatusAndLogoutWorkFromAnyFolderWhileServeOwnsTheDirectory(t *testing.T) {
	owner := startServeOwner(t)
	app, out, _ := otherApp(t)
	ctx := context.Background()
	global := []string{"--data-dir", owner.dir}

	if err := app.Run(ctx, append(append([]string{}, global...), "status")); err != nil {
		t.Fatalf("status was refused: %v", err)
	}
	for _, want := range []string{"wasync serve", "PID", owner.dir} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status does not say which wasync it asked (%q missing):\n%s", want, out.String())
		}
	}

	out.mu.Lock()
	out.buf.Reset()
	out.mu.Unlock()
	if err := app.Run(ctx, append(append([]string{}, global...), "logout")); err != nil {
		t.Fatalf("logout was refused: %v", err)
	}
	for _, want := range []string{"wasync serve", "PID", owner.dir, "Device unlinked", "setup", "login"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("logout output is missing %q:\n%s", want, out.String())
		}
	}
	owner.wait(t, 15*time.Second)
	if owner.held() {
		t.Fatal("the data directory is still locked after logout")
	}
	if !owner.client.loggedOut.Load() {
		t.Fatal("the running owner never unlinked the device")
	}
	// A new phone can now be paired without killing anything.
	pair := &App{Out: &lockedBuffer{}, Err: &lockedBuffer{}, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return &fakeClient{}, nil }}
	if err := pair.Run(ctx, append(append([]string{}, global...), "login", "--select=false")); err != nil {
		t.Fatalf("login after logout: %v", err)
	}
}
