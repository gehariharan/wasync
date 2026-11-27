package agentauth

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Error codes returned to agents. Map Status to the HTTP status code.
const (
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeRateLimited  = "rate_limited"
)

// Error is a typed authorization failure. Message and Help are safe to return
// to the caller: they never contain secrets or hashes. Help describes the human
// action required; agents cannot grant themselves access.
type Error struct {
	Code       string        // unauthorized | forbidden | rate_limited
	Status     int           // 401 | 403 | 429
	Message    string        // short description
	Help       string        // human repair guidance
	Scope      string        // scope that was checked, if any
	Chat       string        // chat that was checked, if any
	RetryAfter time.Duration // rate_limited only
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

const grantHelpDefault = "Ask a human to create an agent credential in an interactive terminal: wasync agents grant NAME --scope read.chats,read.messages. Legacy api.token, send.token and approve.token are not accepted here."

func unauthorized(message string) *Error {
	return &Error{Code: CodeUnauthorized, Status: 401, Message: message, Help: grantHelpDefault}
}

// notAllowedHelp tells an agent what it can do about a chat it may not use.
// Sending there can be asked for one message at a time, which the person
// approves on their own computer; anything else needs the person to sync the
// chat (for an agent that uses the synced chats) or to re-grant the agent.
func notAllowedHelp(c *Client, scope, chat string) string {
	ask := ""
	if scope == ScopeSend {
		ask = fmt.Sprintf(" To send one message there, ask the person to approve it: run `wasync-axi send-request --chat %s --text TEXT` (POST /api/v1/send-requests); nothing is sent until they confirm in wasync.", chat)
	}
	if c.SyncedChats {
		return fmt.Sprintf("Chat %s is not synced in wasync, so this agent cannot use it for %s.%s To read, reply or react there, the person must sync the chat in wasync.", chat, scope, ask)
	}
	regrant := "A human must re-grant the client in an interactive terminal: "
	if ask != "" {
		regrant = "Otherwise a human must re-grant the client in an interactive terminal: "
	}
	return fmt.Sprintf("Chat %s is not in the allowlist for scope %s.%s %s%s", chat, scope, ask, regrant, regrantCommand(c, scope, chat, 0))
}

// RegrantCommand documents the replacement policy shared by these error hints.
func regrantCommand(c *Client, extraScope, extraChat string, rate int) string {
	if c.ExpiresAt != nil {
		return fmt.Sprintf("Ask a human to review wasync agents list and explicitly choose an expiry when replacing expiring agent %s (current expiry %s); retain its chat allowlist and rate limit. Do not create a permanent replacement.", c.Name, c.ExpiresAt.UTC().Format(time.RFC3339))
	}
	set := map[string]bool{}
	for _, s := range c.Scopes {
		set[s] = true
	}
	if extraScope != "" {
		set[extraScope] = true
	}
	chats := []string(nil)
	if !c.SyncedChats {
		// A credential from before agents used the synced chats keeps the
		// chats a person approved for it one by one.
		chats = append(chats, c.Chats...)
		for _, g := range c.Grants {
			chats = append(chats, g.Chat)
		}
	}
	if extraChat != "" && !c.SyncedChats {
		found := false
		for _, existing := range chats {
			found = found || existing == extraChat
		}
		if !found {
			chats = append(chats, extraChat)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wasync agents revoke %s && wasync agents grant %s --scope %s", c.Name, c.Name, strings.Join(canonicalScopes(set), ","))
	needChat := false
	for s := range set {
		needChat = needChat || RequiresAllowlist(s)
	}
	if c.SyncedChats || (len(chats) == 0 && needChat) {
		b.WriteString(" --synced-chats")
	}
	seen := map[string]bool{}
	for _, chat := range chats {
		if !seen[chat] {
			seen[chat] = true
			b.WriteString(" --chat " + chat)
		}
	}
	if rate <= 0 {
		rate = c.RatePerMinute
	}
	if rate != DefaultRatePerMinute {
		b.WriteString(" --rate " + strconv.Itoa(rate))
	}
	return b.String()
}
