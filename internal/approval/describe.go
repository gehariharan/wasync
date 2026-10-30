package approval

import (
	"fmt"
	"strings"

	"github.com/gehariharan/wasync/internal/humancheck"
)

// Effect is the plain-words effect of approving r, written from wasync's own
// data. name is the chat's name when wasync knows it. Names are untrusted
// text, so they are cleaned and shortened before they go on a prompt.
func (r Request) Effect(name string) string {
	chat := quoted(name, r.Chat)
	switch r.Kind {
	case KindChatAccess:
		if r.Access == nil {
			return "grant an agent access to a WhatsApp chat"
		}
		a := r.Access
		for _, c := range a.Candidates {
			if c.Chat == a.Chosen {
				chat = quoted(c.Name, c.Chat)
			}
		}
		what := "read"
		switch {
		case a.Send && a.Edit:
			what = "read, send, reply, react and edit its own messages in"
		case a.Send:
			what = "read, send, reply and react in"
		case a.Edit:
			what = "read and edit its own messages in"
		default:
			what = "read"
		}
		depth := ""
		switch a.History {
		case HistoryNow:
			depth = " (from now on only)"
		case HistoryFull:
			depth = " (full history)"
		}
		return fmt.Sprintf("let agent %s %s the WhatsApp chat %s%s", quoted(a.Agent, "unknown"), what, chat, depth)
	case "clear-message":
		return "remove one message from this computer's archive of " + chat
	case "purge-chat":
		return "permanently erase this computer's archive of " + chat + " and its downloaded files"
	case "revoke-message":
		return "ask WhatsApp to delete YOUR message for everyone in " + chat
	case "delete-whatsapp-chat":
		return "delete YOUR WhatsApp chat " + chat + " from your WhatsApp account"
	}
	return "approve a request"
}

// Prompt is the sentence the operating system shows when asking the person.
func (r Request) Prompt(name string) string { return "Approve: " + r.Effect(name) }

func quoted(name, fallback string) string {
	n := humancheck.Clean(name, 60)
	if n == "" {
		n = humancheck.Clean(fallback, 60)
	}
	return `"` + strings.ReplaceAll(n, `"`, "'") + `"`
}
