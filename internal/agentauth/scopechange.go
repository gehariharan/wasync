package agentauth

import (
	"fmt"
	"time"
)

// Changing what an existing agent may do, without changing its secret. Adding
// a scope widens the agent, so the caller must have the person's confirmation
// (the system prompt) first; removing one only narrows it and needs none. Only
// the chat-bound write scopes below can be changed this way; reading, editing
// and the destructive scopes keep their own routes.
var changeable = map[string]bool{ScopeSend: true}

// CanChangeScope reports whether scope may be added or removed with AddScope
// and RemoveScope.
func CanChangeScope(scope string) bool { return changeable[scope] }

// AddScope gives the active agent called name one more scope. It keeps the
// credential, the chat allowlist and every existing grant exactly as they are,
// and refuses an agent that reads every chat: giving it a write scope would
// mean narrowing its reading to an allowlist, which is a different decision.
func AddScope(dir, name, scope string, now time.Time) (client Client, err error) {
	if err = ValidName(name); err != nil {
		return Client{}, err
	}
	if !CanChangeScope(scope) {
		return Client{}, fmt.Errorf("scope %s cannot be added this way", scope)
	}
	err = update(dir, func(reg *registry) error {
		for i := range reg.Clients {
			r := &reg.Clients[i]
			if r.Name != name || r.RevokedAt != nil || r.Status(now) != "active" {
				continue
			}
			if !r.OnDemand && len(r.Chats) == 0 {
				return fmt.Errorf("agent %s reads every chat, so it cannot be given %s here; re-grant it in a terminal with a chat list", name, scope)
			}
			if !contains(r.Scopes, scope) {
				scopes, perr := ParseScopes(append(append([]string(nil), r.Scopes...), scope)...)
				if perr != nil {
					return perr
				}
				r.Scopes = scopes
			}
			client = cloneClient(r.Client)
			return nil
		}
		return fmt.Errorf("no active agent named %s", name)
	})
	return client, err
}

// RemoveScope takes one scope away from the active agent called name, and
// from every chat grant that carried it. A missing scope is not an error, so
// turning something off twice is harmless.
func RemoveScope(dir, name, scope string, now time.Time) (client Client, err error) {
	if err = ValidName(name); err != nil {
		return Client{}, err
	}
	if !CanChangeScope(scope) {
		return Client{}, fmt.Errorf("scope %s cannot be removed this way", scope)
	}
	err = update(dir, func(reg *registry) error {
		for i := range reg.Clients {
			r := &reg.Clients[i]
			if r.Name != name || r.RevokedAt != nil || r.Status(now) != "active" {
				continue
			}
			r.Scopes = without(r.Scopes, scope)
			grants := r.Grants[:0]
			for _, g := range r.Grants {
				if g.Scopes = without(g.Scopes, scope); len(g.Scopes) > 0 {
					grants = append(grants, g)
				}
			}
			r.Grants = grants
			client = cloneClient(r.Client)
			return nil
		}
		return fmt.Errorf("no active agent named %s", name)
	})
	return client, err
}

func without(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

// AuthorizeOpenSend checks an agent's send to a chat outside its allowlist,
// which is only ever called while the person has switched on open sending for
// this agent. The agent must still hold the send scope and be a valid,
// unrevoked credential; it consumes one rate-limit token like any other
// request, and the attempt is audited either way.
func (a *Authorizer) AuthorizeOpenSend(c *Client, chatJID, requestID string) error {
	err := a.checkOpenSend(c, chatJID)
	entry := AuditEntry{Event: "send_open", Scope: ScopeSend, Chat: chatJID, RequestID: requestID, Outcome: OutcomeAllowed}
	if c != nil {
		entry.ClientID, entry.ClientName = c.ID, c.Name
	}
	if err != nil {
		entry.Outcome, entry.Code = OutcomeDenied, "internal"
		if authErr, ok := err.(*Error); ok {
			entry.Code = authErr.Code
		}
	}
	if auditErr := a.audit.Append(entry); auditErr != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("audit log unavailable; send refused: %w", auditErr)
	}
	return err
}

func (a *Authorizer) checkOpenSend(c *Client, chatJID string) error {
	if c == nil {
		return unauthorized("agent credential required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.refresh(); err != nil {
		return fmt.Errorf("agent registry unavailable: %w", err)
	}
	r, found := a.clients[c.ID]
	now := a.now()
	if !found || r.Status(now) != "active" {
		return unauthorized("agent credential is no longer valid")
	}
	current := r.Client
	if !current.HasScope(ScopeSend) {
		return &Error{Code: CodeForbidden, Status: 403, Scope: ScopeSend, Chat: chatJID, Message: fmt.Sprintf("client %s lacks scope %s", current.Name, ScopeSend), Help: "Ask the person to allow sending for this agent on the Agents page of the wasync web app (wasync web)."}
	}
	if wait := a.take(current, now); wait > 0 {
		seconds := int(wait.Seconds()) + 1
		return &Error{Code: CodeRateLimited, Status: 429, Scope: ScopeSend, Chat: chatJID, RetryAfter: wait, Message: fmt.Sprintf("client %s exceeded %d requests per minute", current.Name, current.RatePerMinute), Help: fmt.Sprintf("Retry after %ds.", seconds)}
	}
	return nil
}
