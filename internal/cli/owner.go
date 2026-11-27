package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/service"
)

// An owner is the one wasync process that holds a data directory (see
// processlock). Any other command run against that directory, from any folder,
// either works through the owner's local API or says plainly who the owner is
// and how to stop it.

// ownerStopWait is how long a command waits for an owner to finish shutting down.
const ownerStopWait = 20 * time.Second

// ownerBusyError is returned when another wasync holds the data directory and
// the command cannot work through it. It still matches processlock.ErrBusy.
type ownerBusyError struct{ message string }

func (e *ownerBusyError) Error() string        { return e.message }
func (e *ownerBusyError) Is(target error) bool { return target == processlock.ErrBusy }

// credentialError means the approval credential could not be read, so no
// request was sent. No other request may stand in for the approval; only stop
// goes on without it, by asking its own user's process directly.
type credentialError struct{ err error }

func (e *credentialError) Error() string { return e.err.Error() }
func (e *credentialError) Unwrap() error { return e.err }

// startedBy says how this process was started, when that can be told.
func (a *App) startedBy() string {
	getenv := a.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if a.desktopApp() != "" {
		return startedByApp
	}
	if runtime.GOOS == "darwin" {
		switch name := getenv("XPC_SERVICE_NAME"); name {
		case service.Label:
			return "login item"
		case "", "0":
			return "manually"
		}
	}
	return ""
}

// shellWord quotes s for a shell only when it needs it.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandFor writes a wasync command the way the person should type it for
// this data directory.
func commandFor(cfg config.Config, args string) string {
	if def, err := config.Default(); err != nil || def.DataDir != cfg.DataDir {
		return "wasync --data-dir " + shellWord(cfg.DataDir) + " " + args
	}
	return "wasync " + args
}

// describeOwner names a process in one line: what it is, its PID, how it was
// started, where its data is and where its API listens.
func describeOwner(o processlock.Owner) string {
	what := "wasync " + o.Kind
	switch o.Kind {
	case "serve":
		what += " (background capture, no window)"
	case "tui", "run":
		what += " (the full-screen window)"
	}
	parts := []string{fmt.Sprintf("PID %d", o.PID)}
	if o.StartedBy != "" {
		parts = append(parts, "started by the "+o.StartedBy)
		if o.StartedBy == "manually" {
			parts[len(parts)-1] = "started manually"
		}
	}
	if !o.StartedAt.IsZero() {
		parts = append(parts, "since "+o.StartedAt.Local().Format("2006-01-02 15:04"))
	}
	parts = append(parts, "data directory "+o.DataDir)
	if o.APIAddr != "" {
		parts = append(parts, "local API "+o.APIAddr)
	}
	return what + " [" + strings.Join(parts, ", ") + "]"
}

// liveOwner returns the record of the process that holds the data directory
// now. It is false when nothing holds it, or when the holder did not say who it is.
func liveOwner(cfg config.Config) (processlock.Owner, bool) {
	held, err := processlock.Held(filepath.Join(cfg.DataDir, "device.lock"))
	if err != nil || !held {
		return processlock.Owner{}, false
	}
	return processlock.ReadOwner(cfg.DataDir)
}

// withOwnerAPI points cfg at the API address the owner itself recorded, so a
// command works even when the owner was started with a different --api-addr.
func withOwnerAPI(cfg config.Config) config.Config {
	if owner, ok := liveOwner(cfg); ok && owner.APIAddr != "" {
		cfg.APIAddr = owner.APIAddr
	}
	return cfg
}

// unknownOwner explains a held directory whose holder left no record.
func unknownOwner(cfg config.Config, doing string) error {
	return &ownerBusyError{fmt.Sprintf("another wasync is using the data directory %s but did not say which one it is, so wasync cannot %s through it. "+
		"It is either a command that only just started (try again in a moment) or an older wasync that does not report itself: quit that wasync (close its window, or end its process) and try again", cfg.DataDir, doing)}
}

// busyError says who holds the data directory and what to do about it for a
// command that cannot share it.
func (a *App) busyError(cfg config.Config, command string) error {
	owner, ok := liveOwner(cfg)
	if !ok {
		return unknownOwner(cfg, "run "+command)
	}
	if !owner.LongLived() {
		return &ownerBusyError{fmt.Sprintf("wasync %s cannot start: %s is running right now and finishes by itself. Try again in a moment.", command, describeOwner(owner))}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wasync %s cannot start: %s already owns this data directory.\n", command, describeOwner(owner))
	switch command {
	case "login":
		fmt.Fprintf(&b, "To pair a different phone, unlink this one through the running wasync with %q, which also stops it.\n", commandFor(cfg, "logout"))
	case "serve", "tui", "run", "setup":
		fmt.Fprintf(&b, "Use the wasync that is already running, or stop it first.\n")
	}
	fmt.Fprintf(&b, "Stop it with: %s", commandFor(cfg, "stop"))
	if owner.StartedBy == "login item" {
		fmt.Fprintf(&b, "\nIt was started by the login item, so it starts again at your next login; remove that with: wasync service uninstall")
	}
	return &ownerBusyError{b.String()}
}

// channel is a verified way to reach the owner of a data directory.
type channel struct {
	cfg   config.Config // with the owner's own API address
	owner processlock.Owner
}

// reachOwner finds the live owner and checks it has a local command channel.
func (a *App) reachOwner(cfg config.Config, doing string) (channel, error) {
	owner, ok := liveOwner(cfg)
	if !ok {
		return channel{}, unknownOwner(cfg, doing)
	}
	if !owner.LongLived() {
		return channel{}, &ownerBusyError{fmt.Sprintf("%s is running right now and finishes by itself; try again in a moment.", describeOwner(owner))}
	}
	if owner.APIAddr == "" {
		return channel{}, &ownerBusyError{fmt.Sprintf("%s has no local command channel (it runs without the API, or is still starting), so wasync cannot %s through it.\nStop it with: %s", describeOwner(owner), doing, commandFor(cfg, "stop"))}
	}
	cfg.APIAddr = owner.APIAddr
	return channel{cfg: cfg, owner: owner}, nil
}

// call sends one signed request to the owner. credential names the token file
// the owner checks it against. The body always names the owner's PID, so a
// different wasync that took over the address refuses it.
func (a *App) call(ctx context.Context, ch channel, method, path, credential string, extra map[string]any) (int, []byte, error) {
	token, err := a.readCredential(ch.cfg, credential)
	if err != nil {
		return 0, nil, &credentialError{err}
	}
	var body []byte
	if method == "POST" {
		payload := map[string]any{"pid": ch.owner.PID}
		for k, v := range extra {
			payload[k] = v
		}
		body, _ = json.Marshal(payload)
	}
	response, err := commandRequest(ctx, ch.cfg, method, path, body, token)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, data, err
}

// unreachable explains that the owner exists but did not answer.
func unreachable(cfg config.Config, ch channel, doing string, err error) error {
	return &ownerBusyError{fmt.Sprintf("could not reach %s to %s: %v\nIf it is stuck, stop it with: %s", describeOwner(ch.owner), doing, err, commandFor(cfg, "stop"))}
}

func apiError(data []byte, code int) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("HTTP %d", code)
}

// waitReleased waits for the owner to let go of the data directory.
func waitReleased(ctx context.Context, cfg config.Config, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		held, err := processlock.Held(filepath.Join(cfg.DataDir, "device.lock"))
		if err == nil && !held {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// logoutThroughOwner unlinks the phone using the wasync that is running, then
// leaves the data directory free so a new phone can be paired.
func (a *App) logoutThroughOwner(ctx context.Context, cfg config.Config) error {
	ch, err := a.reachOwner(cfg, "unlink the device")
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Another wasync owns this data directory, so logout is going through it: %s.\n", describeOwner(ch.owner))
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	code, data, err := a.call(ctx, ch, "POST", "/api/logout", approvecred.FileName, nil)
	if err != nil {
		return unreachable(cfg, ch, "unlink the device", err)
	}
	switch {
	case code == 409 && strings.Contains(apiError(data, code), "no saved session"):
		fmt.Fprintln(a.Out, "No saved session to unlink.")
		return nil
	case code != 200:
		return fmt.Errorf("the running wasync did not unlink the device: %s", apiError(data, code))
	}
	fmt.Fprintln(a.Out, "Device unlinked. Cached group preferences remain local.")
	if waitReleased(ctx, cfg, ownerStopWait) {
		fmt.Fprintf(a.Out, "That wasync has stopped, because it cannot capture without a linked phone. To pair a new phone, run %q (in your browser) or %q.\n", commandFor(cfg, "setup"), commandFor(cfg, "login"))
	} else {
		fmt.Fprintf(a.Out, "That wasync is still shutting down. Once it has, run %q or %q to pair a new phone; if it does not stop, run %q.\n", commandFor(cfg, "setup"), commandFor(cfg, "login"), commandFor(cfg, "stop"))
	}
	return nil
}

// statusThroughOwner reports the state held by the wasync that is running.
func (a *App) statusThroughOwner(ctx context.Context, cfg config.Config) error {
	ch, err := a.reachOwner(cfg, "report status")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	code, data, err := a.call(ctx, ch, "GET", "/api/owner", "api.token", nil)
	if err != nil {
		return unreachable(cfg, ch, "report status", err)
	}
	if code != 200 {
		return unreachable(cfg, ch, "report status", errors.New(apiError(data, code)))
	}
	var who struct {
		Owner struct {
			PID int `json:"pid"`
		} `json:"owner"`
		Connection string `json:"connection"`
		Paired     *bool  `json:"paired"`
	}
	if err = json.Unmarshal(data, &who); err != nil || who.Owner.PID != ch.owner.PID {
		return unreachable(cfg, ch, "report status", errors.New("the address answered as a different wasync"))
	}
	var counts struct {
		Enabled  int   `json:"enabled_groups"`
		Archived int64 `json:"archived_messages"`
	}
	if code, data, err = a.call(ctx, ch, "GET", "/api/status", "api.token", nil); err == nil && code == 200 {
		_ = json.Unmarshal(data, &counts)
	}
	fmt.Fprintf(a.Out, "Another wasync owns this data directory, so status is read from it: %s.\n", describeOwner(ch.owner))
	if who.Paired != nil {
		fmt.Fprintf(a.Out, "Saved session: %t (reported by the running wasync)\n", *who.Paired)
	}
	fmt.Fprintf(a.Out, "Connection: %s\nData directory: %s\nEnabled groups: %d; archived messages: %d\n", who.Connection, cfg.DataDir, counts.Enabled, counts.Archived)
	fmt.Fprintf(a.Out, "To stop it: %s. History completeness: unknown.\n", commandFor(cfg, "stop"))
	return nil
}

// runStop stops the wasync that owns the data directory, so nobody needs to
// find its process and end it by hand.
func (a *App) runStop(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: wasync stop")
	}
	owner, stopped, err := a.stopOwner(ctx, cfg)
	if err != nil || !stopped {
		return err
	}
	if owner.StartedBy == "login item" {
		fmt.Fprintln(a.Out, "It was started by the login item. Start it again with: wasync service restart (or it starts at your next login; remove that with: wasync service uninstall)")
	}
	return nil
}

// stopOwner stops the wasync that owns the data directory and waits until the
// directory is free. stopped is false when nothing needed stopping, or the
// owner is a short command that finishes by itself (still running).
func (a *App) stopOwner(ctx context.Context, cfg config.Config) (owner processlock.Owner, stopped bool, err error) {
	held, err := processlock.Held(filepath.Join(cfg.DataDir, "device.lock"))
	if err != nil {
		return owner, false, err
	}
	if !held {
		fmt.Fprintf(a.Out, "No wasync is running for the data directory %s.\n", cfg.DataDir)
		return owner, false, nil
	}
	owner, ok := processlock.ReadOwner(cfg.DataDir)
	if !ok {
		return owner, false, unknownOwner(cfg, "stop it")
	}
	if !owner.LongLived() {
		fmt.Fprintf(a.Out, "%s is running right now and finishes by itself; try again in a moment.\n", describeOwner(owner))
		return owner, false, nil
	}
	return owner, true, a.stopLongLived(ctx, cfg, owner)
}

// stopLongLived asks a long-lived owner to stop and waits until it has.
//
// The owner's stop route needs the approval credential because anything that
// can reach the local API (another device through tailscale serve, a page in
// the browser, an agent holding a narrower credential) could otherwise stop
// capture. This command does not need that route: it runs as the person who
// owns the process, and that alone lets it ask the process to stop as Ctrl-C
// or kill would. So when the credential cannot be read here - on macOS it is in
// the login Keychain, which a shell outside the desktop session (SSH, say)
// cannot open - the command asks the process instead of failing.
func (a *App) stopLongLived(ctx context.Context, cfg config.Config, owner processlock.Owner) (err error) {
	fmt.Fprintf(a.Out, "Stopping %s.\n", describeOwner(owner))
	stopped := false
	var apiFailure, noCredential error
	if owner.APIAddr != "" {
		ch := channel{cfg: cfg, owner: owner}
		ch.cfg.APIAddr = owner.APIAddr
		callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		code, data, err := a.call(callCtx, ch, "POST", "/api/stop", approvecred.FileName, nil)
		cancel()
		var denied *credentialError
		switch {
		case errors.As(err, &denied):
			noCredential = err
		case err != nil:
			apiFailure = err
		case code/100 == 2:
			stopped = true
		default:
			return fmt.Errorf("%s answered but did not stop: %s.\nwasync stop only works through an owner that accepts the approval. Quit it in its own window, or end process %d yourself (Task Manager on Windows)", describeOwner(owner), apiError(data, code), owner.PID)
		}
	}
	if !stopped && apiFailure != nil && waitReleased(ctx, cfg, time.Second) {
		// The owner may have exited before its reply reached us; the lock says it is gone.
		stopped = true
	}
	if !stopped {
		if owner.PID == os.Getpid() {
			// Never signal ourselves: nothing legitimate stops by this route.
			reason := apiFailure
			if noCredential != nil {
				reason = noCredential
			}
			return fmt.Errorf("%s could not be stopped through its local API (%v), and wasync will not signal its own process", describeOwner(owner), reason)
		}
		if noCredential != nil {
			fmt.Fprintf(a.Out, "The approval credential cannot be read here (%v), so wasync is asking process %d to stop, as Ctrl-C would.\n", noCredential, owner.PID)
		}
		if err = processlock.Terminate(owner.PID); err != nil {
			switch {
			case noCredential != nil:
				err = fmt.Errorf("%w (the approval credential for its local API could not be read: %v)", err, noCredential)
			case apiFailure != nil:
				err = fmt.Errorf("%w (its local API said: %v)", err, apiFailure)
			}
			return fmt.Errorf("%w; end process %d yourself (Task Manager on Windows)", err, owner.PID)
		}
	}
	if !waitReleased(ctx, cfg, ownerStopWait) {
		return fmt.Errorf("asked %s to stop, but it still holds the data directory after %s; it may be finishing work, so run %q again in a moment", describeOwner(owner), ownerStopWait, commandFor(cfg, "stop"))
	}
	fmt.Fprintln(a.Out, "Stopped. The data directory is free.")
	return nil
}
