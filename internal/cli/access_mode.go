package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/config"
)

// accessUsage documents `wasync access mode`.
const accessModeUsage = `Usage: wasync access mode [ask | all [--history now|full]]

Show or change how agents get chats. Human only: changing it needs an
interactive terminal, a typed confirmation and a running wasync owner (the TUI
or serve); there is no --yes and no API route an agent can reach.

  ask   (default) every agent request for a chat waits for you to approve it,
        choosing the chat, read or send, and the history depth.
  all   "sync all chats": turns on capture for every chat now (--history picks
        from now on only, or a full history sync) and from then on approves an
        agent's request for the one chat it matched at once, without asking:
        agents can READ, SEND, REPLY and REACT in any chat. Editing their own
        messages and deleting stay manual. Each approval is still recorded as an
        approval request and in the audit log.

Switching back to ask stops future automatic approvals; access already given
stays until you revoke the agent. Without an argument the current mode is shown.
`

func (a *App) runAccessMode(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args) || args[0] != "mode" {
		if len(args) > 0 && isHelpArg(args) {
			_, err := fmt.Fprint(a.Out, accessModeUsage)
			return err
		}
		return fmt.Errorf("usage: wasync access mode [ask | all [--history now|full]]")
	}
	name, rest := splitName(args[1:])
	fs := a.flags("access mode")
	history := fs.String("history", "", "with all: now (from now on only) or full (full history sync)")
	if err := a.parseFlags(fs, rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync access mode [ask | all [--history now|full]]")
	}
	if name == "" {
		return a.showAccessMode(cfg)
	}
	mode := accessmode.Mode{Mode: name, History: *history}
	if mode.Mode == accessmode.Ask && mode.History != "" {
		return fmt.Errorf("--history only applies to all")
	}
	if mode.Mode != accessmode.Ask && mode.Mode != accessmode.All {
		return fmt.Errorf("mode must be %q or %q", accessmode.Ask, accessmode.All)
	}
	if !a.Interactive {
		return fmt.Errorf("changing the access mode requires an interactive terminal; nothing changed")
	}
	phrase := "ASK"
	if mode.Mode == accessmode.All {
		if mode.History == "" {
			fmt.Fprint(a.Err, "History depth for every chat:\n  1) From now on only (new messages; nothing older requested)\n  2) Full history sync (also asks your phone for every chat's older messages, best effort, can be large)\nChoose [1-2]: ")
			line, err := readLine(ctx, a.In)
			if err != nil {
				return err
			}
			switch strings.TrimSpace(line) {
			case "1":
				mode.History = accessmode.HistoryNow
			case "2":
				mode.History = accessmode.HistoryFull
			default:
				return fmt.Errorf("no history depth chosen; nothing changed")
			}
		}
		if err := mode.Validate(); err != nil {
			return err
		}
		depth := map[string]string{accessmode.HistoryNow: "from now on only", accessmode.HistoryFull: "full history sync"}[mode.History]
		fmt.Fprintf(a.Err, "\nSYNC ALL CHATS (history: %s)\nThis turns on capture for every chat now. From then on any agent that asks for a chat is\napproved at once, without asking you: it can find any chat by name or number (a match is granted at once, so a name probe both reveals and grants that chat) and READ, SEND, REPLY and REACT in any chat.\nEditing its own messages and deleting stay manual; every approval is audited.\n", depth)
		phrase = "SYNC ALL"
	} else {
		fmt.Fprintln(a.Err, "Ask each time: agent requests for chats wait for your approval from now on.\nAccess already given stays until you revoke the agent.")
	}
	fmt.Fprintf(a.Err, "Type %s then Enter to confirm: ", phrase)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != phrase {
		return fmt.Errorf("confirmation did not match; nothing changed")
	}
	body, err := json.Marshal(mode)
	if err != nil {
		return err
	}
	return a.ownerCommand(ctx, cfg, "POST", "/api/access/mode", body, "approve.token")
}

// showAccessMode prints the saved mode; it reads the private file directly so
// it works whether or not an owner is running.
func (a *App) showAccessMode(cfg config.Config) error {
	m, chosen, err := accessmode.Load(cfg.DataDir)
	if err != nil {
		return err
	}
	text := "ask each time (default; not chosen yet)"
	switch {
	case m.Mode == accessmode.All:
		text = "sync all chats, history: " + map[string]string{accessmode.HistoryNow: "from now on only", accessmode.HistoryFull: "full history sync"}[m.History] + "; agents' read, send, reply and react requests are approved automatically"
	case chosen:
		text = "ask each time"
	}
	_, err = fmt.Fprintf(a.Out, "access mode: %s\n", text)
	return err
}
