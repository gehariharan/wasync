// Package agentauth implements scoped agent credentials for the versioned API.
//
// Agent credentials are completely separate from the legacy api.token,
// send.token and approve.token: Authenticate accepts only wsa_ credentials and
// the legacy routes compare against their own tokens, so neither kind is ever
// accepted in place of the other. Credentials are created, listed and revoked
// only by a human through the wasync CLI (an interactive terminal is required
// for grant and revoke); nothing in this package is reachable over HTTP.
package agentauth

import (
	"fmt"
	"sort"
	"strings"
)

// Grantable scopes. There is deliberately no "destructive" scope: revoke,
// delete, purge and clean always go through the human approval queue.
const (
	ScopeReadChats     = "read.chats"
	ScopeReadMessages  = "read.messages"
	ScopeReadMedia     = "read.media"
	ScopeObserve       = "observe"
	ScopeSend          = "send"
	ScopeReact         = "react"
	ScopeEditOwn       = "edit.own"
	ScopeChatState     = "chat.state"
	ScopeGroupAdmin    = "group.admin"
	ScopeCaptureManage = "capture.manage"
	ScopeExport        = "export"
)

// AllScopes lists every grantable scope in display order.
var AllScopes = []string{ScopeReadChats, ScopeReadMessages, ScopeReadMedia, ScopeObserve, ScopeSend, ScopeReact, ScopeEditOwn, ScopeChatState, ScopeGroupAdmin, ScopeCaptureManage, ScopeExport}

// DefaultScopes is granted when the human supplies no --scope: read only.
// observe is separate and must be requested explicitly.
var DefaultScopes = []string{ScopeReadChats, ScopeReadMessages}

var allowlistRequired = map[string]bool{ScopeSend: true, ScopeReact: true, ScopeEditOwn: true, ScopeChatState: true, ScopeGroupAdmin: true, ScopeCaptureManage: true}

// Scope families a just-in-time access request can ask for. Read access always
// comes with a request; send (send, which covers replies, and react) and edit
// (edit.own) are separate optional extras. Capture, group, state and export
// scopes are never requestable: only a human grant can add them.
var (
	readFamily = []string{ScopeReadChats, ScopeReadMessages, ScopeReadMedia, ScopeObserve}
	sendFamily = []string{ScopeSend, ScopeReact}
	editFamily = []string{ScopeEditOwn}
)

func requestable(scope string) bool {
	return contains(readFamily, scope) || contains(sendFamily, scope) || contains(editFamily, scope)
}

// AccessScopes returns the scopes an access request for read (plus send and/or
// edit when asked) would grant client: those of the requested families that its
// credential already holds. A request can never widen the credential itself.
func (c *Client) AccessScopes(send, edit bool) ([]string, error) {
	set := map[string]bool{}
	add := func(family []string) bool {
		held := false
		for _, s := range family {
			if c.HasScope(s) {
				set[s] = true
				held = true
			}
		}
		return held
	}
	if !add(readFamily) {
		return nil, fmt.Errorf("this credential holds no read scope")
	}
	if send && !add(sendFamily) {
		return nil, fmt.Errorf("this credential holds no send or react scope")
	}
	if edit && !add(editFamily) {
		return nil, fmt.Errorf("this credential holds no edit.own scope")
	}
	return canonicalScopes(set), nil
}

// IsRequestable reports whether an access request could ever cover scope.
func IsRequestable(scope string) bool { return requestable(scope) }

// ValidScope reports whether s can be granted.
func ValidScope(s string) bool {
	for _, known := range AllScopes {
		if s == known {
			return true
		}
	}
	return false
}

// RequiresAllowlist reports whether granting or using scope needs a chat allowlist.
func RequiresAllowlist(scope string) bool { return allowlistRequired[scope] }

// ParseScopes parses comma-separated scope lists (possibly repeated), removing
// duplicates and returning them in canonical order. Empty input yields DefaultScopes.
func ParseScopes(values ...string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "destructive" {
				return nil, fmt.Errorf("scope destructive cannot be granted; destructive actions always require human approval")
			}
			if !ValidScope(part) {
				return nil, fmt.Errorf("unknown scope %q; valid scopes: %s", part, strings.Join(AllScopes, ","))
			}
			seen[part] = true
		}
	}
	if len(seen) == 0 {
		return append([]string(nil), DefaultScopes...), nil
	}
	return canonicalScopes(seen), nil
}

func canonicalScopes(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for _, s := range AllScopes {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// ValidName enforces client names of [a-z0-9-]{1,32}.
func ValidName(name string) error {
	if len(name) < 1 || len(name) > 32 {
		return fmt.Errorf("client name must be 1-32 characters of a-z, 0-9 and -")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("client name must be 1-32 characters of a-z, 0-9 and -")
		}
	}
	return nil
}

// ValidChatJID performs a conservative syntactic check of a chat JID.
func ValidChatJID(jid string) error {
	at := strings.IndexByte(jid, '@')
	if len(jid) == 0 || len(jid) > 128 || at <= 0 || at == len(jid)-1 || strings.Count(jid, "@") != 1 {
		return fmt.Errorf("invalid chat JID %q; expected user@server, e.g. 123456789-123@g.us", jid)
	}
	for _, r := range jid {
		if r <= ' ' || r == 0x7f || r == '\'' || r == '"' || r == '\\' || r == '`' || r == '$' {
			return fmt.Errorf("invalid chat JID %q", jid)
		}
	}
	return nil
}

func normalizeChats(chats []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range chats {
		c = strings.TrimSpace(c)
		if err := ValidChatJID(c); err != nil {
			return nil, err
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}
