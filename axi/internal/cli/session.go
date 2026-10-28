package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/axi/internal/agentsetup"
	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/sessionlog"
)

// `session-end` is the session-end capture of AXI principle 7. The hook that
// `setup hooks` installs runs it when an agent session ends. It records which
// whatsapp-axi commands the session ran and how often (names and counts,
// never arguments, chat names or message text), so the next session's
// `--brief` can show it. It never talks to wasync.

const sessionEndName = "session-end"

const sessionEndUsage = "whatsapp-axi session-end [--json]"

var sessionEndCommand = &command{
	name:        sessionEndName,
	usage:       sessionEndUsage,
	description: "Run by the session-end hook (see `setup hooks`): records the names and counts of whatsapp-axi commands the ended session ran, never arguments or chat content; reads the hook's JSON from stdin",
	flags:       []flagSpec{jsonFlag},
	examples:    []string{"echo '{\"cwd\":\"/work/project\",\"transcript_path\":\"/path/transcript.jsonl\"}' | whatsapp-axi session-end"},
	run:         (*App).runSessionEnd,
}

// sessionPayload is what the hooks send. Claude Code and Codex name a
// transcript to read; the OpenCode plugin sends the command names it saw.
type sessionPayload struct {
	Cwd            string   `json:"cwd"`
	TranscriptPath string   `json:"transcript_path"`
	Commands       []string `json:"commands"`
}

// loggedCommand reports whether a command name belongs in the session record:
// every command except the hook itself and setup.
func loggedCommand(name string) bool {
	if name == sessionEndName || name == "setup" {
		return false
	}
	return findCommand(name) != nil
}

func (a *App) sessionLogPath() string {
	dir := config.ConfigDir(a.Getenv)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, sessionlog.File)
}

func (a *App) getwd() string {
	if a.Getwd == nil {
		return ""
	}
	dir, _ := a.Getwd()
	return dir
}

func (a *App) runSessionEnd(_ context.Context, inv invocation) error {
	if a.Stdin == nil {
		return usageErr("this command is run by an agent's session-end hook; install it with `"+setupHooksFix+"`", "session-end reads the hook's JSON from stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(a.Stdin, 1<<20))
	if err != nil {
		return usageErr("usage: "+sessionEndUsage, "cannot read the hook payload from stdin")
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return usageErr("usage: "+sessionEndUsage, "stdin is not the hook's JSON payload")
	}
	counts := sessionlog.Names(p.Commands, loggedCommand)
	if len(p.Commands) == 0 && p.TranscriptPath != "" {
		if f, err := os.Open(p.TranscriptPath); err == nil {
			if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
				counts = sessionlog.FromTranscript(f, loggedCommand)
			}
			_ = f.Close()
		}
	}
	dir := p.Cwd
	if dir == "" {
		dir = a.getwd()
	}
	path := a.sessionLogPath()
	recorded := len(counts) > 0 && dir != "" && path != ""
	if recorded {
		if err := sessionlog.Save(path, dir, sessionlog.Record{At: time.Now().UTC().Truncate(time.Second), Commands: counts}); err != nil {
			return contract.Errorf(agentsetup.CodeWriteFailed, "check that the whatsapp-axi config directory is writable", "cannot record the session: %v", err)
		}
	}
	if inv.json {
		return output.JSON(a.Out, map[string]any{"recorded": recorded, "commands": counts})
	}
	o := output.New(a.Out)
	if recorded {
		o.Field(0, "session", "recorded "+sessionlog.Summary(sessionlog.Record{Commands: counts}))
	} else {
		o.Field(0, "session", "no whatsapp-axi commands to record")
	}
	return o.Err()
}

// lastSession is the previous session's record for the current directory.
func (a *App) lastSession() (sessionlog.Record, bool) {
	dir, path := a.getwd(), a.sessionLogPath()
	if dir == "" || path == "" {
		return sessionlog.Record{}, false
	}
	return sessionlog.Last(path, dir)
}
