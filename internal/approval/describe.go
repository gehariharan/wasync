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
	case KindAgentSend:
		if r.Send == nil {
			return "send a message for an agent"
		}
		where := chat + ", " + r.Send.Target()
		if r.Send.NewNumber {
			where = "the new number " + quoted(PhoneNumber(r.Chat), r.Chat)
		}
		return fmt.Sprintf("let agent %s send this message to %s: %s", quoted(r.Send.Agent, "unknown"), where, quotedText(r.Send.Text))
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

// PhoneNumber is the phone number of a direct chat with a phone-number
// address, such as +15551230000 for 15551230000@s.whatsapp.net, or "" for any
// other chat.
func PhoneNumber(chat string) string {
	user, server, ok := strings.Cut(chat, "@")
	if !ok || server != "s.whatsapp.net" || user == "" {
		return ""
	}
	for _, r := range user {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "+" + user
}

// quotedText is an agent's message, cleaned and shortened for a prompt. The
// person sees all of it on the approval page; the prompt shows how it starts.
func quotedText(text string) string {
	n := humancheck.Clean(text, 200)
	if n == "" {
		n = "(no text)"
	}
	return `"` + strings.ReplaceAll(n, `"`, "'") + `"`
}

func quoted(name, fallback string) string {
	n := humancheck.Clean(name, 60)
	if n == "" {
		n = humancheck.Clean(fallback, 60)
	}
	return `"` + strings.ReplaceAll(n, `"`, "'") + `"`
}
