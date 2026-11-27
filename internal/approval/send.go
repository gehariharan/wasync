package approval

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/whatsapp"
)

// KindAgentSend is an agent's request to send one message to a chat it may
// not use: a chat that is not synced, or a phone number the archive has never
// seen. It is the only thing an agent asks the person for. Replying and
// reacting in synced chats never asks. Approving it sends exactly the text the
// agent gave, once, to that one chat; it grants the agent nothing further.
// Declining or letting it expire changes nothing.
const KindAgentSend = "agent-send"

const (
	// MaxSendText is the longest text a send request may carry, so the person
	// can read all of it before approving.
	MaxSendText = 4096
	// MaxPendingSendsPerAgent is how many send requests one agent may have
	// waiting at once.
	MaxPendingSendsPerAgent = 5
	maxPendingAll           = 32
)

// AgentSend is the payload of a KindAgentSend request. The target chat is the
// request's Chat.
type AgentSend struct {
	Agent   string `json:"agent"`
	AgentID string `json:"agent_id"`
	Text    string `json:"text"` // untrusted text from the agent, sent as is once approved
	// NewNumber marks a phone number the archive had no chat with when the
	// agent asked.
	NewNumber bool `json:"new_number,omitempty"`
	// Synced marks a chat that was synced in wasync when the agent asked,
	// though not one this agent may use.
	Synced bool `json:"synced,omitempty"`
	// MessageID is the WhatsApp message ID once wasync sent it.
	MessageID string `json:"message_id,omitempty"`
}

func validateSend(chat string, s AgentSend) error {
	if s.Agent == "" || s.AgentID == "" {
		return fmt.Errorf("agent required")
	}
	if _, err := whatsapp.ParseChat(chat); err != nil {
		return err
	}
	if strings.TrimSpace(s.Text) == "" {
		return fmt.Errorf("text required")
	}
	if len(s.Text) > MaxSendText {
		return fmt.Errorf("text exceeds %d bytes", MaxSendText)
	}
	return nil
}

// Target says what kind of chat an agent's send request goes to, for the
// person: one that was not synced when the agent asked, or a synced chat this
// agent may not use. A new number is described by its number instead.
func (s AgentSend) Target() string {
	if s.Synced {
		return "a synced chat this agent may not use"
	}
	return "a chat that was not synced when it asked"
}

// IsAgentRequest reports whether an agent raised r, as opposed to the owner's
// own delete confirmations, which stay a separate list.
func (r Request) IsAgentRequest() bool { return r.Kind == KindAgentSend && r.Send != nil }

func (r Request) clone() Request {
	if r.Send != nil {
		s := *r.Send
		r.Send = &s
	}
	return r
}

// AddSend queues an agent's request to send s.Text to chat. Asking again for
// the same message to the same chat while one is pending returns that request
// (created is false) instead of asking the person twice, and one agent can
// have only MaxPendingSendsPerAgent requests waiting at once.
func (q *Queue) AddSend(account int64, chat string, s AgentSend) (r Request, created bool, err error) {
	s.MessageID = ""
	if err = validateSend(chat, s); err != nil {
		return Request{}, false, err
	}
	q.mu.Lock()
	r, created, err = q.addSendLocked(account, chat, s)
	notify := q.OnSend
	q.mu.Unlock()
	if err == nil && created && notify != nil {
		notify(r)
	}
	return r, created, err
}

func (q *Queue) addSendLocked(account int64, chat string, s AgentSend) (Request, bool, error) {
	q.expire()
	active, perAgent := 0, 0
	for _, r := range q.requests {
		if r.Status != StatusPending {
			continue
		}
		active++
		if r.Account == account && r.IsAgentRequest() && r.Send.AgentID == s.AgentID {
			perAgent++
			if r.Chat == chat && r.Send.Text == s.Text {
				return r.clone(), false, nil
			}
		}
	}
	if perAgent >= MaxPendingSendsPerAgent {
		return Request{}, false, fmt.Errorf("this agent already has %d send requests waiting for the person to answer", perAgent)
	}
	if active >= maxPendingAll {
		return Request{}, false, fmt.Errorf("too many pending requests")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Request{}, false, err
	}
	r := Request{ID: hex.EncodeToString(id), Account: account, Input: Input{Kind: KindAgentSend, Chat: chat}, Send: &s, Expires: time.Now().Add(RequestTTL), Status: StatusPending}
	q.evictOld()
	q.requests = append(q.requests, r)
	q.save()
	return r.clone(), true, nil
}

// AgentRequests returns the account's agent requests still waiting for the
// person, oldest first. The owner's own delete confirmations are not among
// them.
func (q *Queue) AgentRequests(account int64) []Request {
	out := []Request{}
	for _, r := range q.PendingList(account) {
		if r.IsAgentRequest() {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Expires.Before(out[j].Expires) })
	return out
}

// RejectAgentRequests declines every agent request waiting for the account's
// person at once ("Not now to all"). Saying
// no needs no confirmation; approving is always one request at a time. The
// owner's own delete confirmations are left as they are. It returns the
// requests it declined.
func (q *Queue) RejectAgentRequests(account int64) []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	var declined []Request
	for i, r := range q.requests {
		if r.Account == account && r.Status == StatusPending && r.IsAgentRequest() {
			q.requests[i].Status = StatusRejected
			declined = append(declined, q.requests[i].clone())
		}
	}
	if len(declined) > 0 {
		q.save()
	}
	return declined
}

// MarkSent records the WhatsApp message ID of an approved send while it is
// being carried out, so the agent can see what was sent.
func (q *Queue) MarkSent(id, messageID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.requests {
		if q.requests[i].ID == id && q.requests[i].Send != nil {
			q.requests[i].Send.MessageID = messageID
			return
		}
	}
}

// Reject closes a pending request without acting on it.
func (q *Queue) Reject(account int64, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire()
	for i, r := range q.requests {
		if r.Account == account && r.ID == id && r.Status == StatusPending {
			q.requests[i].Status = StatusRejected
			q.save()
			return nil
		}
	}
	return fmt.Errorf("request expired or already handled")
}
