package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/service"
)

// The wasync app (cmd/wasync-desktop) is a window and a menu-bar or tray icon
// around wasync's own local pages. It starts `wasync setup --app` and talks to
// it over that process's standard input and output, which only the app holds:
// that is how it gets the one-time links that open the pages in its window,
// without the approval credential and without a link ever being printed. When
// the app quits, or goes away, standard input closes and this wasync stops.

// DesktopAppEnv names the wasync app's own program. The app sets it for the
// wasync it starts, so "start when I log in" opens the app rather than
// starting wasync serve.
const DesktopAppEnv = "WASYNC_DESKTOP_APP"

// startedByApp is how an owner started by the wasync app describes itself.
const startedByApp = "wasync app"

// AppMessage is one line on the app channel, in either direction.
type AppMessage struct {
	// Event is set on messages from wasync: "ready" once its local API answers,
	// "link" in reply to a link request, "error" for a request it refused.
	Event string `json:"event,omitempty"`
	// Op is set on requests from the app: "link".
	Op string `json:"op,omitempty"`
	// Page is the page a link opens: "", "setup" or "chats".
	Page string `json:"page,omitempty"`
	// URL is the one-time link (link) or the local API address (ready).
	URL   string `json:"url,omitempty"`
	PID   int    `json:"pid,omitempty"`
	Error string `json:"error,omitempty"`
}

// appPages are the pages the app may open; they are the ones a one-time link can name.
var appPages = map[string]bool{"": true, "setup": true, "chats": true}

// serveAppChannel answers the app until it closes standard input, then calls
// stop. Each request gets exactly one reply line.
func serveAppChannel(ctx context.Context, in io.Reader, out io.Writer, apiURL string, pid int, newLink func(string) string, stop func()) {
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	send := func(m AppMessage) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(m)
	}
	send(AppMessage{Event: "ready", URL: apiURL, PID: pid})
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024), 4<<10)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		var req AppMessage
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			send(AppMessage{Event: "error", Error: "not a JSON request"})
			continue
		}
		switch {
		case req.Op == "link" && appPages[req.Page]:
			send(AppMessage{Event: "link", Page: req.Page, URL: newLink(req.Page)})
		case req.Op == "link":
			send(AppMessage{Event: "error", Op: req.Op, Error: `page must be "", "setup" or "chats"`})
		default:
			send(AppMessage{Event: "error", Op: req.Op, Error: "unknown request"})
		}
	}
	// The app quit or went away: nothing can open these pages any more.
	stop()
}

// desktopApp is the wasync app's program when this wasync was started by it.
func (a *App) desktopApp() string {
	exe := a.getenv(DesktopAppEnv)
	if exe == "" || !filepath.IsAbs(exe) {
		return ""
	}
	return exe
}

// loginItem is the start-at-login item this owner offers on its setup page:
// the app's own item when the app started it, otherwise the wasync serve item.
func (a *App) loginItem(cfg config.Config) (service.Manager, service.Spec, bool) {
	if exe := a.desktopApp(); exe != "" {
		dir, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return nil, service.Spec{}, false
		}
		mgr := a.AppService
		if mgr == nil {
			mgr = service.App()
		}
		return mgr, service.Spec{Exe: exe, DataDir: dir}, true
	}
	if a.Service == nil {
		return nil, service.Spec{}, false
	}
	spec, err := a.serviceSpec(cfg)
	if err != nil {
		return nil, service.Spec{}, false
	}
	return a.Service, spec, true
}

// OwnerStatus is what the wasync app shows about the wasync that holds a data
// directory, read from that wasync's local API with the bearer token.
type OwnerStatus struct {
	// Running is set when a wasync holds the data directory.
	Running bool
	// Answered is set when it answered on its local API.
	Answered  bool
	Kind      string
	PID       int
	StartedBy string
	// Paired is nil when it did not say (it has no WhatsApp connection).
	Paired      *bool
	Connection  string
	SyncedChats int
}

// Status reports the wasync that holds cfg's data directory. A wasync that
// holds it but does not answer yet is Running without Answered.
func (a *App) Status(ctx context.Context, cfg config.Config) OwnerStatus {
	owner, ok := liveOwner(cfg)
	if !ok {
		return OwnerStatus{}
	}
	st := OwnerStatus{Running: true, Kind: owner.Kind, PID: owner.PID, StartedBy: owner.StartedBy}
	ch, err := a.reachOwner(cfg, "report status")
	if err != nil {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, data, err := a.call(ctx, ch, "GET", "/api/owner", "api.token", nil)
	if err != nil || code != 200 {
		return st
	}
	var who struct {
		Owner struct {
			PID int `json:"pid"`
		} `json:"owner"`
		Connection string `json:"connection"`
		Paired     *bool  `json:"paired"`
	}
	if json.Unmarshal(data, &who) != nil || who.Owner.PID != owner.PID {
		return st
	}
	st.Answered, st.Connection, st.Paired = true, who.Connection, who.Paired
	var counts struct {
		Chats int `json:"enabled_chats"`
	}
	if code, data, err = a.call(ctx, ch, "GET", "/api/status", "api.token", nil); err == nil && code == 200 && json.Unmarshal(data, &counts) == nil {
		st.SyncedChats = counts.Chats
	}
	return st
}

// PageLink asks the wasync that holds cfg's data directory for a one-time link
// to a page, signed with the approval credential, and returns it to the caller
// only. The app uses it to show the pages of a wasync it did not start.
func (a *App) PageLink(ctx context.Context, cfg config.Config, page string) (string, error) {
	if !appPages[page] {
		return "", fmt.Errorf(`page must be "", "setup" or "chats"`)
	}
	return a.pageLink(ctx, withOwnerAPI(cfg), page)
}
