// Package desktop is the part of the wasync app that needs no window: it
// starts wasync for the app or finds the one already running, gets one-time
// links to its pages, says in a few words how it is doing, and stops it when
// the app quits. cmd/wasync-desktop puts a window and a menu-bar or tray icon
// around it.
package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/cli"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/firstopen"
	"github.com/gehariharan/wasync/internal/uninstall"
	"github.com/gehariharan/wasync/internal/update"
)

// ErrStopped means wasync is not running and the app is quitting, so nothing
// will start it again.
var ErrStopped = errors.New("wasync is stopping")

// Host runs wasync for the app.
type Host struct {
	// Wasync is the wasync command the app starts (`wasync setup --app`).
	Wasync string
	// App is the app's own program; wasync's start-at-login opens it.
	App string
	// Config holds the data directory and the local API address; see DataDir.
	Config config.Config
	// Log receives what the wasync the app started says for a person.
	Log io.Writer
	// CLI reads the status of, and links from, a wasync the app did not start.
	CLI *cli.App
	// Command builds the process to start; nil uses exec.Command. Tests and
	// Windows (which hides the console window) replace it.
	Command func(name string, args ...string) *exec.Cmd
	// ReadyWait is how long a started wasync has to answer; zero means a minute.
	ReadyWait time.Duration
	// OnUpdate is called when the wasync the app started stops to install an
	// update; the app quits then, and the update opens the new app.
	OnUpdate func()
	// OnRemove is called when the wasync the app started stops because the
	// person is removing wasync; the app quits then, so the removal can move
	// or uninstall it.
	OnRemove func()

	mu       sync.Mutex
	child    *child
	quitting bool
	// lastExit says why the last wasync the app started stopped, if it failed.
	lastExit string
	failures int
}

// child is one wasync the app started, and its two pipes.
type child struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	replies chan cli.AppMessage
	ready   chan struct{} // closed once it says it is ready
	done    chan struct{} // closed once it has exited
	err     error         // how it exited, once done is closed
	pid     int
	asking  sync.Mutex // one request at a time, so replies match requests
}

func (c *child) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Ensure makes sure a wasync runs for the data directory: the one already
// running, or one the app starts and waits for. own reports which.
func (h *Host) Ensure(ctx context.Context) (own bool, err error) {
	h.mu.Lock()
	if h.quitting {
		h.mu.Unlock()
		return false, ErrStopped
	}
	c := h.child
	h.mu.Unlock()
	if c == nil || !c.alive() {
		if st := h.CLI.Status(ctx, h.config()); st.Running {
			return h.useOther(), nil
		}
		if c, err = h.start(); err != nil {
			return false, err
		}
	}
	if err = h.waitReady(ctx, c); err != nil && !c.alive() && h.CLI.Status(ctx, h.config()).Running {
		return h.useOther(), nil
	}
	return true, err
}

// DataDir is the data directory in use. It follows a move of the data out of
// the folder earlier versions used that another wasync finishes after the
// app started.
func (h *Host) DataDir() string { return config.Resolve(h.Config.DataDir) }

func (h *Host) config() config.Config {
	c := h.Config
	c.DataDir = h.DataDir()
	return c
}

// useOther notes that another wasync holds the data directory, so no failed
// wasync of the app's own is waiting to start again. It reports own as false.
func (h *Host) useOther() bool {
	h.mu.Lock()
	h.lastExit, h.failures = "", 0
	h.mu.Unlock()
	return false
}

func (h *Host) waitReady(ctx context.Context, c *child) error {
	wait := h.ReadyWait
	if wait == 0 {
		wait = time.Minute
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-c.ready:
		return nil
	case <-c.done:
		if why := h.LastExit(); why != "" {
			return fmt.Errorf("wasync stopped while starting: %s", why)
		}
		return errors.New("wasync stopped while starting without saying why")
	case <-timer.C:
		return fmt.Errorf("wasync did not start within %s", wait)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Host) command(name string, args ...string) *exec.Cmd {
	if h.Command != nil {
		return h.Command(name, args...)
	}
	return exec.Command(name, args...)
}

// start starts `wasync setup --app` and reads its replies until it exits.
func (h *Host) start() (*child, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.quitting {
		return nil, ErrStopped
	}
	if h.child != nil && h.child.alive() {
		return h.child, nil
	}
	cmd := h.command(h.Wasync, "--data-dir", h.DataDir(), "--api-addr", h.Config.APIAddr, "setup", "--app")
	cmd.Env = append(cmd.Environ(), cli.DesktopAppEnv+"="+h.App)
	cmd.Stderr = h.Log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start wasync: %w", err)
	}
	c := &child{cmd: cmd, stdin: stdin, replies: make(chan cli.AppMessage, 1), ready: make(chan struct{}), done: make(chan struct{}), pid: cmd.Process.Pid}
	h.child = c
	started := time.Now()
	go func() {
		readReplies(stdout, c)
		c.err = cmd.Wait()
		h.exited(c, time.Since(started))
	}()
	return c, nil
}

// readReplies hands each line wasync writes to whoever is waiting for it.
func readReplies(r io.Reader, c *child) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024), 64<<10)
	readyOnce := sync.Once{}
	for scanner.Scan() {
		var m cli.AppMessage
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			continue
		}
		if m.Event == "ready" {
			readyOnce.Do(func() { close(c.ready) })
			continue
		}
		select {
		case c.replies <- m:
		default: // nobody asked; drop it rather than block wasync
		}
	}
}

// restartAfter is how long the app waits before starting a wasync that failed
// again: a little longer after each failure in a row, up to a minute.
func restartAfter(failures int) time.Duration {
	d := time.Second << min(failures, 6)
	return min(d, time.Minute)
}

// exited notes how a wasync the app started stopped. One that failed is
// started again after a pause; one that stopped cleanly (wasync stop, or the
// phone was unlinked) stays stopped until the person opens the app again.
// One that stopped to be updated or removed quits the app instead.
// It closes c.done only once the exit is recorded and an update handed over,
// so whoever sees wasync stopped also sees why and what came of it.
func (h *Host) exited(c *child, ran time.Duration) {
	h.mu.Lock()
	if h.child == c {
		h.child = nil
	}
	if c.err == nil {
		h.lastExit, h.failures = "", 0
		quitting := h.quitting
		h.mu.Unlock()
		switch {
		case quitting:
		case h.OnUpdate != nil && update.Pending(h.DataDir()):
			h.OnUpdate()
		case h.removing():
			h.OnRemove()
		}
		close(c.done)
		return
	}
	if ran > 2*time.Minute {
		h.failures = 0
	}
	h.failures++
	h.lastExit = c.err.Error()
	wait, quitting := restartAfter(h.failures), h.quitting
	close(c.done)
	h.mu.Unlock()
	if quitting {
		return
	}
	// Starting wasync again would hold the data directory the removal waits for.
	if h.removing() {
		h.OnRemove()
		return
	}
	time.AfterFunc(wait, func() { _, _ = h.Ensure(context.Background()) })
}

// removing reports whether the app should quit because wasync is being removed.
func (h *Host) removing() bool {
	return h.OnRemove != nil && uninstall.Pending(h.DataDir())
}

// LastExit says why the last wasync the app started failed, or "".
func (h *Host) LastExit() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastExit
}

// Link returns a one-time link to a page of the running wasync, starting it first if needed.
func (h *Host) Link(ctx context.Context, page string) (string, error) {
	own, err := h.Ensure(ctx)
	if err != nil {
		return "", err
	}
	if !own {
		return h.CLI.PageLink(ctx, h.Config, page)
	}
	h.mu.Lock()
	c := h.child
	h.mu.Unlock()
	if c == nil {
		return "", errors.New("wasync stopped")
	}
	c.asking.Lock()
	defer c.asking.Unlock()
	select {
	case <-c.replies:
	default:
	}
	req, _ := json.Marshal(cli.AppMessage{Op: "link", Page: page})
	if _, err = c.stdin.Write(append(req, '\n')); err != nil {
		return "", fmt.Errorf("ask wasync for a link: %w", err)
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case m := <-c.replies:
			if m.Event == "link" && m.Page != page {
				continue
			}
			if m.Event != "link" || m.URL == "" {
				return "", fmt.Errorf("wasync gave no link: %s", m.Error)
			}
			return m.URL, nil
		case <-c.done:
			return "", errors.New("wasync stopped")
		case <-timeout.C:
			return "", errors.New("wasync did not answer")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// StartLink returns a one-time link to the start page, starting wasync first
// if needed.
func (h *Host) StartLink(ctx context.Context) (string, error) {
	if _, err := h.Ensure(ctx); err != nil {
		return "", err
	}
	return h.Link(ctx, h.StartPage(ctx))
}

// StartPage is the page the window opens on: setup while the First open
// screen is still to be seen or no phone is linked, and the chats otherwise.
// The First open screen itself belongs to the setup pages; until they show
// it, this opens setup.
func (h *Host) StartPage(ctx context.Context) string {
	if pending, err := firstopen.Pending(h.DataDir()); err != nil || pending {
		return "setup"
	}
	if st := h.CLI.Status(ctx, h.config()); st.Paired == nil || !*st.Paired {
		return "setup"
	}
	return "chats"
}

// Status is the line the menu-bar or tray menu shows.
func (h *Host) Status(ctx context.Context) string {
	h.mu.Lock()
	own := h.child != nil && h.child.alive()
	lastExit := h.lastExit
	h.mu.Unlock()
	return Describe(h.CLI.Status(ctx, h.config()), own, lastExit)
}

// Describe says in a few words how wasync is doing.
func Describe(st cli.OwnerStatus, own bool, lastExit string) string {
	switch {
	case !st.Running && lastExit != "":
		return "wasync stopped after a problem; trying again"
	case !st.Running:
		return "wasync is not running"
	case !st.Answered:
		return "wasync is starting…"
	}
	var line string
	switch {
	case st.Paired != nil && !*st.Paired:
		line = "Not linked to WhatsApp yet"
	case st.Connection == "online" && st.SyncedChats == 1:
		line = "Syncing 1 chat"
	case st.Connection == "online":
		line = fmt.Sprintf("Syncing %d chats", st.SyncedChats)
	case st.Connection == "connecting" || st.Connection == "reconnecting" || st.Connection == "pairing":
		line = "Connecting to WhatsApp…"
	case st.Connection == "attention required" || st.Connection == "capture stopped" || st.Connection == "unlinked":
		line = "WhatsApp needs attention: open wasync"
	default:
		line = "Offline"
	}
	if !own {
		line += " (in another wasync)"
	}
	return line
}

// Stop stops the wasync the app started, waiting up to wait for it to finish
// cleanly before ending it, and starts nothing again. A wasync the app did
// not start is left running.
func (h *Host) Stop(wait time.Duration) error {
	h.mu.Lock()
	h.quitting = true
	c := h.child
	h.mu.Unlock()
	if c == nil || !c.alive() {
		return nil
	}
	_ = c.stdin.Close() // wasync stops cleanly when the app lets go
	select {
	case <-c.done:
		return nil
	case <-time.After(wait):
	}
	if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-c.done
	return fmt.Errorf("wasync did not stop within %s and was ended", wait)
}
