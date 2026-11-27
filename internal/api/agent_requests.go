package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
)

// The person's side of agent send requests, for the approval pages and the
// desktop app: list the requests waiting (oldest first, with when each
// lapses), approve one, decline one, or decline them all. Approving is always
// one request at a time, each with its own system confirmation that names
// exactly that message; declining needs no confirmation. The owner's own
// delete confirmations are not agent requests and are not listed here.
//
// The JSON routes use the same signed requests as /api/actions: approving
// needs the approve credential, listing and declining the send credential.
const agentRequestsPath = "/api/agent-requests"

// agentRequestView is one agent send request as the person sees it. Agent,
// ChatName and Text come from agents and WhatsApp, so they are untrusted text,
// cleaned for display.
type agentRequestView struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Chat      string    `json:"chat"`
	ChatName  string    `json:"chat_name,omitempty"`
	Phone     string    `json:"phone,omitempty"` // the number, for a phone-number chat
	NewNumber bool      `json:"new_number"`
	Synced    bool      `json:"synced"` // the chat was synced, though not one this agent may use
	Text      string    `json:"text"`
	AskedAt   time.Time `json:"asked_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Effect is the sentence the system confirmation shows for approving it.
	Effect string `json:"effect"`
}

// agentRequests lists the account's waiting agent send requests, oldest first.
func (h *handler) agentRequests(ctx context.Context, account int64) []agentRequestView {
	if h.approvals == nil {
		return []agentRequestView{}
	}
	names := map[string]string{}
	if groups, err := h.store.Groups(ctx, account); err == nil {
		for _, g := range groups {
			names[g.ID] = g.Name
		}
	}
	out := []agentRequestView{}
	for _, r := range h.approvals.AgentRequests(account) {
		name := names[r.Chat]
		out = append(out, agentRequestView{
			ID: r.ID, Agent: humancheck.Clean(r.Send.Agent, 60), Chat: r.Chat, ChatName: humancheck.Clean(name, 80), Phone: approval.PhoneNumber(r.Chat),
			NewNumber: r.Send.NewNumber, Synced: r.Send.Synced, Text: humancheck.CleanLines(r.Send.Text), AskedAt: r.Expires.Add(-approval.RequestTTL), ExpiresAt: r.Expires, Effect: r.Effect(name),
		})
	}
	return out
}

// agentRequest returns the waiting agent send request id, or nil.
func (h *handler) agentRequest(account int64, id string) *approval.Request {
	if h.approvals == nil {
		return nil
	}
	r := h.approvals.Get(account, id)
	if r == nil || !r.IsAgentRequest() || r.Status != approval.StatusPending {
		return nil
	}
	return r
}

// declineAgentRequests declines every waiting agent send request ("Not now to
// all") and audits each; it returns how many it declined.
func (h *handler) declineAgentRequests(account int64) int {
	declined := h.approvals.RejectAgentRequests(account)
	for _, r := range declined {
		_ = h.agents.Audit(agentauth.AuditEntry{Event: "send_declined", ClientID: r.Send.AgentID, ClientName: r.Send.Agent, Scope: agentauth.ScopeSend, Chat: r.Chat, Ref: r.ID[:8], Outcome: agentauth.OutcomeDenied})
	}
	return len(declined)
}

func (h *handler) listAgentRequests(w http.ResponseWriter, r *http.Request) {
	account := h.account(w, r)
	if account == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"items": h.agentRequests(r.Context(), account.ID)})
}

func (h *handler) approveAgentRequest(w http.ResponseWriter, r *http.Request) {
	if h.approve == nil {
		fail(w, 503, "interactive approval unavailable")
		return
	}
	account := h.account(w, r)
	if account == nil {
		return
	}
	id := r.PathValue("id")
	if h.agentRequest(account.ID, id) == nil {
		fail(w, 404, "no such agent request waiting")
		return
	}
	if err := h.approveRequest(r.Context(), account.ID, id); err != nil {
		failWith(w, err)
		return
	}
	writeJSON(w, 200, h.approvals.Get(account.ID, id))
}

func (h *handler) declineAgentRequest(w http.ResponseWriter, r *http.Request) {
	account := h.account(w, r)
	if account == nil {
		return
	}
	id := r.PathValue("id")
	if h.agentRequest(account.ID, id) == nil || h.reject == nil {
		fail(w, 404, "no such agent request waiting")
		return
	}
	if err := h.reject(account.ID, id); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"declined": 1})
}

func (h *handler) declineAllAgentRequests(w http.ResponseWriter, r *http.Request) {
	account := h.account(w, r)
	if account == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"declined": h.declineAgentRequests(account.ID)})
}
