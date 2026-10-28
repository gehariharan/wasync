package approval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/whatsapp"
)

// KindChatAccess is an agent's request for access to a chat it may not use yet.
// Unlike the destructive kinds it grants nothing until a human picks the chat
// and the history depth, then confirms; rejection and expiry change nothing.
const KindChatAccess = "grant-chat-access"

// History depth the human chooses when approving a chat access request.
const (
	HistoryNow  = "now"  // capture from now on; no older messages are requested
	HistoryFull = "full" // also ask the phone for the chat's older messages
)

const (
	maxCandidates      = 10
	maxQueryLen        = 256
	maxPendingPerAgent = 3
)

// ErrNoCandidates means a request matched no chat, so there is nothing for a
// human to approve. It is reported to the agent at once rather than queued.
var ErrNoCandidates = fmt.Errorf("no chat matches the request")

// Candidate is one concrete chat an access request could mean. Candidates are
// resolved by wasync from the archive and shown only to the human, never to the
// agent, so the agent learns nothing about which chats matched.
type Candidate struct {
	Chat string `json:"chat"`
	Name string `json:"name"`
}

// Access is the payload of a KindChatAccess request.
type Access struct {
	Agent      string      `json:"agent"`
	AgentID    string      `json:"agent_id"`
	Send       bool        `json:"send"`   // read is always included; send is the optional extra
	Scopes     []string    `json:"scopes"` // exactly what approval grants, within the credential's own scopes
	Query      string      `json:"query"`  // what the agent asked for; untrusted text for display
	Candidates []Candidate `json:"candidates"`
	Truncated  bool        `json:"truncated,omitempty"` // more chats matched than are listed
	// Chosen and History are the human's decision, recorded while pending.
	Chosen  string `json:"chosen_chat,omitempty"`
	History string `json:"history,omitempty"`
}

func (a Access) validate() error {
	if a.Agent == "" || a.AgentID == "" || len(a.Scopes) == 0 {
		return fmt.Errorf("agent and scopes required")
	}
	if len(a.Candidates) == 0 {
		return ErrNoCandidates
	}
	if len(a.Query) > maxQueryLen || len(a.Candidates) > maxCandidates {
		return fmt.Errorf("access request too large")
	}
	for _, c := range a.Candidates {
		if _, err := whatsapp.ParseChat(c.Chat); err != nil {
			return err
		}
	}
	return nil
}

// Visible reports whether a human can act on the request. A request that
// matched no chat is kept, so the agent cannot tell "no such chat" from "the
// person has not answered", but it is never shown and cannot be approved.
func (r Request) visible() bool {
	return r.Kind != KindChatAccess || (r.Access != nil && len(r.Access.Candidates) > 0)
}

func (r Request) clone() Request {
	if r.Access != nil {
		a := *r.Access
		a.Scopes = append([]string(nil), a.Scopes...)
		a.Candidates = append([]Candidate(nil), a.Candidates...)
		r.Access = &a
	}
	return r
}

func normalizeQuery(q string) string { return strings.ToLower(strings.Join(strings.Fields(q), " ")) }

// AddAccess queues an access request. Asking again for the same thing while a
// request is pending returns that request instead of prompting the human twice,
// and one agent can hold only a few pending requests at once.
func (q *Queue) AddAccess(account int64, a Access) (Request, error) {
	if err := a.validate(); err != nil {
		return Request{}, err
	}
	a.Chosen, a.History = "", ""
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	active, perAgent := 0, 0
	for _, r := range q.requests {
		if r.Status != StatusPending {
			continue
		}
		active++
		if r.Account == account && r.Kind == KindChatAccess && r.Access.AgentID == a.AgentID {
			perAgent++
			if r.Access.Send == a.Send && normalizeQuery(r.Access.Query) == normalizeQuery(a.Query) {
				return r.clone(), nil
			}
		}
	}
	if perAgent >= maxPendingPerAgent {
		return Request{}, fmt.Errorf("this agent already has %d access requests waiting for the person to answer", perAgent)
	}
	if active >= 32 {
		return Request{}, fmt.Errorf("too many pending requests")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Request{}, err
	}
	in := Input{Kind: KindChatAccess}
	if len(a.Candidates) == 1 {
		in.Chat = a.Candidates[0].Chat
	}
	r := Request{ID: hex.EncodeToString(id), Account: account, Input: in, Access: &a, Expires: time.Now().Add(5 * time.Minute), Status: StatusPending}
	q.evictOld()
	q.requests = append(q.requests, r)
	return r.clone(), nil
}

// Decide records the human's choice of chat and history depth on a pending
// access request. The choice applies only to this request and only when it is
// then approved with Take; it can be changed until then.
func (q *Queue) Decide(account int64, id, chat, history string) error {
	if history != HistoryNow && history != HistoryFull {
		return fmt.Errorf("history must be %q or %q", HistoryNow, HistoryFull)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	for i, r := range q.requests {
		if r.Account != account || r.ID != id || r.Status != StatusPending || r.Kind != KindChatAccess {
			continue
		}
		for _, c := range r.Access.Candidates {
			if c.Chat == chat {
				q.requests[i].Access.Chosen, q.requests[i].Access.History = chat, history
				return nil
			}
		}
		return fmt.Errorf("chat is not one of the candidates for this request")
	}
	return fmt.Errorf("request expired or already handled")
}

// Reject closes a pending request without acting on it.
func (q *Queue) Reject(account int64, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	for i, r := range q.requests {
		if r.Account == account && r.ID == id && r.Status == StatusPending {
			q.requests[i].Status = "rejected"
			return nil
		}
	}
	return fmt.Errorf("request expired or already handled")
}
