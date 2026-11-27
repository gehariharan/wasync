package api

import (
	"net/http"

	"github.com/gehariharan/wasync/internal/update"
)

// appStarted is how an owner started by the wasync app describes itself
// (internal/cli startedByApp).
const appStarted = "wasync app"

// The four setup steps (decision D1): link the phone, choose chats to sync,
// keep wasync running, connect an agent. They share a plain top bar and a
// centred stepper instead of the rail; the rail appears once setup is done.
const (
	stepLink = iota + 1
	stepChats
	stepRunning
	stepAgent
)

var setupSteps = []struct {
	label, path string
}{
	{"Link", "/ui/setup/link"},
	{"Chats", "/ui/setup/chats"},
	{"Keep running", "/ui/setup/running"},
	{"Agent", "/ui/setup/agent"},
}

// stepView is one step in the stepper.
type stepView struct {
	N             int
	Label, Href   string
	Done, Current bool
}

// setupFrame is what the top bar and the stepper show.
type setupFrame struct {
	Steps []stepView
	// Status is the top bar's pill ("Running", "Waiting for your phone");
	// Tone colours it: "ok", "wait" or "bad".
	Status, Tone string
}

// setupFrameFor builds the frame for step current. Steps before it are done
// and can be gone back to; a later step opens only once a phone is linked.
func (h *handler) setupFrameFor(current int, paired bool) *setupFrame {
	f := &setupFrame{Status: "Running", Tone: "ok"}
	if !paired {
		f.Status, f.Tone = "Waiting for your phone", "wait"
	}
	for i, s := range setupSteps {
		n := i + 1
		v := stepView{N: n, Label: s.label, Done: n < current, Current: n == current}
		if n != current && (paired || n == stepLink) {
			v.Href = s.path
		}
		f.Steps = append(f.Steps, v)
	}
	return f
}

// agentOnly reports whether wasync is used for AI agents only, so its own
// chat screens are hidden. A damaged settings file keeps the default.
func (h *handler) agentOnly() bool {
	s, _ := h.prefs.Get()
	return s.AgentOnly()
}

// homePath is where the app opens once set up: Home for agents only, the
// chats otherwise.
func (h *handler) homePath() string {
	if h.agentOnly() {
		return "/ui/home"
	}
	return "/ui/chats"
}

// updateNote is the line every page shows once a check found a newer wasync
// that installs in one click. Nothing checks by itself, so it appears only
// after a check the person asked for; Settings says how to update otherwise.
func (h *handler) updateNote(r *http.Request) string {
	if h.update == nil || r.URL.Path == "/ui/settings" {
		return ""
	}
	s := h.update.Status()
	if s.State != update.StateAvailable || s.Latest == nil || !s.CanInstall {
		return ""
	}
	return "wasync " + s.Latest.Version + " is ready to install."
}
