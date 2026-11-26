package agentauth

import "fmt"

// Reading the replies to an approved recipient list. When the person gives an
// agent a list of phone numbers to message, the agent may also read the chats
// of the numbers its list has messaged, so it can see the replies (and any
// STOP), without the person approving each chat. Only the read scopes the
// agent already holds apply there; a list never adds a scope, and never
// covers media, the change feed or any write.
var listReadScopes = map[string]bool{ScopeReadChats: true, ScopeReadMessages: true}

// ListReadable reports whether scope may be used on a chat the agent reads
// because of its approved recipient list.
func ListReadable(scope string) bool { return listReadScopes[scope] }

// AuthorizeListRead checks an agent's read that reaches chats outside its
// allowlist which the caller found among the agent's list chats (see
// Client.ListChats): the one chat a request names, or, for a request over
// every chat it may see (a chat list or a search), each list chat the
// allowlist does not already cover. The scope must be one ListReadable allows
// and that the credential holds; the read consumes one rate-limit token like
// any other request. Every attempt is audited as read_list, one entry for each
// chat, allowed or denied, so the person can see what the list let the agent
// read, and an allowed read is refused when the audit log cannot be written.
func (a *Authorizer) AuthorizeListRead(c *Client, scope string, chats []string, ref, requestID string) error {
	entry := AuditEntry{Event: "read_list", Scope: scope, Ref: ref, RequestID: requestID}
	if !ListReadable(scope) {
		err := &Error{Code: CodeForbidden, Status: 403, Scope: scope, Message: fmt.Sprintf("scope %s does not apply to an approved list's chats", scope), Help: "An approved list lets its agent read the chats of the numbers it messaged, nothing more."}
		if len(chats) == 1 {
			err.Chat = chats[0]
		}
		entry.Outcome, entry.Code = OutcomeDenied, err.Code
		if c != nil {
			entry.ClientID, entry.ClientName = c.ID, c.Name
		}
		for _, chat := range chats {
			entry.Chat = chat
			_ = a.audit.Append(entry)
		}
		return err
	}
	return a.authorizeWide(c, entry, chats, "read", fmt.Sprintf("Missing scope %s. An approved list never adds a scope; a human must re-grant the agent with it.", scope))
}
