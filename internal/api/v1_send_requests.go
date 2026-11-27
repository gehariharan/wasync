package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// Agent send requests: the one thing an agent asks the person for. A
// connected agent reads, replies and reacts in every synced chat without
// asking; to message a chat that is not synced, or a phone number the archive
// has never seen, it asks here with the exact text. The person sees the
// agent, the chat and the whole message, and approves that one message with
// the computer's own check (or declines it); wasync then sends it once.
// Asking grants nothing, and a request expires after approval.RequestTTL.
//
// Per-chat access requests, from before agents used every synced chat, are
// gone; their routes answer 410 with what to do instead.
func init() {
	registerV1(v1Route{Pattern: "POST /api/v1/send-requests", Handle: (*handler).v1RequestSend})
	registerV1(v1Route{Pattern: "/api/v1/send-requests", Handle: v1MethodNotAllowed("POST", `use POST /api/v1/send-requests with {"chat":"CHAT_JID","text":"..."} or {"to":"PHONE_NUMBER","text":"..."}`)})
	registerV1(v1Route{Pattern: "GET /api/v1/send-requests/{id}", Handle: (*handler).v1SendRequestStatus})
	registerV1(v1Route{Pattern: "/api/v1/send-requests/{id}", Handle: v1MethodNotAllowed("GET, HEAD", "use GET /api/v1/send-requests/{id}")})
	registerV1(v1Route{Pattern: "/api/v1/access-requests", Handle: (*handler).v1AccessRequestsGone})
	registerV1(v1Route{Pattern: "/api/v1/access-requests/{id}", Handle: (*handler).v1AccessRequestsGone})
	query.RegisterFeature("write.send.request")
}

// accessRequestsGone is the help a caller of the removed access requests gets.
const accessRequestsGone = "A connected agent already reads, replies and reacts in every chat synced in wasync; to use another chat, the person syncs it in wasync. To message a chat that is not synced, or a new number, ask the person to approve that message: POST /api/v1/send-requests (wasync-axi send-request)."

func (h *handler) v1AccessRequestsGone(w http.ResponseWriter, _ *http.Request, id string) {
	v1Fail(w, id, http.StatusGone, query.CodeInvalidArgument, "per-chat access requests were replaced", accessRequestsGone)
}

// sendRequestView is the agent-facing state of a send request.
type sendRequestView struct {
	RequestID   string     `json:"request_id,omitempty"`
	Status      string     `json:"status"` // pending, sent, declined, expired, failed or already_allowed
	Chat        string     `json:"chat"`
	NewNumber   bool       `json:"new_number"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	MessageID   string     `json:"message_id,omitempty"`
	ApproveWith string     `json:"approve_with,omitempty"`
}

func sendRequestStatus(status string) string {
	switch status {
	case approval.StatusPending, "executing":
		return "pending"
	case "completed":
		return "sent"
	case approval.StatusRejected:
		return "declined"
	case "expired":
		return "expired"
	}
	return "failed"
}

func (h *handler) v1RequestSend(w http.ResponseWriter, r *http.Request, id string) {
	c := v1Client(r)
	if h.approvals == nil {
		v1Fail(w, id, 503, query.CodeOwnerOffline, "no wasync owner is running to ask the person", "ask the person to start wasync; nothing was requested")
		return
	}
	var in struct {
		Chat string `json:"chat"`
		To   string `json:"to"`
		Text string `json:"text"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "invalid request body", `send {"chat":"CHAT_JID","text":"..."} or {"to":"PHONE_NUMBER","text":"..."}; a send request carries text only`)
		return
	}
	chat, to := strings.TrimSpace(in.Chat), strings.TrimSpace(in.To)
	switch {
	case (chat == "") == (to == ""):
		v1Fail(w, id, 400, query.CodeInvalidArgument, "give exactly one of chat or to", `send {"chat":"CHAT_JID","text":"..."} or {"to":"PHONE_NUMBER","text":"..."}`)
		return
	case strings.TrimSpace(in.Text) == "" || len(in.Text) > approval.MaxSendText:
		v1Fail(w, id, 400, query.CodeInvalidArgument, "text must be 1 to "+strconv.Itoa(approval.MaxSendText)+" bytes", "a send request carries the exact text the person approves")
		return
	}
	target := chat
	if target == "" {
		var ok bool
		if target, ok = numberJID(to); !ok {
			v1Fail(w, id, 400, query.CodeInvalidArgument, "to must be a phone number with its country code, such as +15551230000", "")
			return
		}
	} else if _, err := whatsapp.ParseChat(target); err != nil {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "chat must be an exact chat JID such as 123456789-123@g.us or 15551234567@s.whatsapp.net", "or use to with a phone number")
		return
	}
	if err := h.agents.AllowRequest(c); err != nil {
		var denied *agentauth.Error
		if errors.As(err, &denied) {
			v1Fail(w, id, denied.Status, denied.Code, denied.Message, denied.Help)
		} else {
			v1Fail(w, id, 500, query.CodeInternal, "agent registry unavailable", "check permissions of agents.json in the wasync data directory")
		}
		return
	}
	if !c.HasScope(agentauth.ScopeSend) {
		_ = h.agents.Audit(agentauth.AuditEntry{Event: "send_request", ClientID: c.ID, ClientName: c.Name, Scope: agentauth.ScopeSend, Chat: target, Outcome: agentauth.OutcomeDenied, Code: query.CodeForbidden, RequestID: id})
		v1Fail(w, id, 403, query.CodeForbidden, "this credential cannot send", "a human must connect or re-grant the agent with the send scope; asking to send needs it too")
		return
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return
	}
	if h.mayAlreadySend(r.Context(), c, target) {
		v1OK(w, id, sendRequestView{Status: "already_allowed", Chat: target}, nil)
		return
	}
	known, synced, err := h.knownChat(r.Context(), target)
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive unavailable", "check the wasync owner log")
		return
	}
	req, _, err := h.approvals.AddSend(a.ID, target, approval.AgentSend{Agent: c.Name, AgentID: c.ID, Text: in.Text, NewNumber: !known && approval.PhoneNumber(target) != "", Synced: synced})
	if err != nil {
		v1Fail(w, id, 429, query.CodeRateLimited, err.Error(), "wait for the person to answer or for the requests to expire ("+strconv.Itoa(int(approval.RequestTTL/time.Minute))+" minutes)")
		return
	}
	if err = h.agents.Audit(agentauth.AuditEntry{Event: "send_request", ClientID: c.ID, ClientName: c.Name, Scope: agentauth.ScopeSend, Chat: target, Ref: req.ID[:8], Outcome: agentauth.OutcomeAllowed, RequestID: id}); err != nil {
		_ = h.approvals.Reject(a.ID, req.ID)
		v1Fail(w, id, 500, query.CodeInternal, "audit log unavailable; nothing was requested", "check permissions of agent-audit.log in the wasync data directory")
		return
	}
	exp := req.Expires
	writeJSON(w, http.StatusAccepted, query.V1Response{RequestID: id, Data: sendRequestView{RequestID: req.ID, Status: "pending", Chat: target, NewNumber: req.Send.NewNumber, ExpiresAt: &exp, ApproveWith: "wasync actions approve " + req.ID}})
}

// mayAlreadySend reports whether POST /api/v1/messages would let c send to
// chat without asking right now: a chat it may use, or a number its approved
// list or open sending has room for.
func (h *handler) mayAlreadySend(ctx context.Context, c *agentauth.Client, chat string) bool {
	if c.AllowsChatScope(agentauth.ScopeSend, chat) {
		return true
	}
	var no *sendRefusal
	if list, on := sendlist.For(h.dataDir, c.ID, time.Now()); on && list.Find(chat) != nil {
		_, no = h.listSendCheck(ctx, c, chat)
	} else {
		_, no = h.openSendCheck(ctx, chat)
	}
	return no == nil
}

func (h *handler) v1SendRequestStatus(w http.ResponseWriter, r *http.Request, id string) {
	c := v1Client(r)
	if h.approvals == nil {
		v1Fail(w, id, 503, query.CodeOwnerOffline, "no wasync owner is running", "ask the person to start wasync")
		return
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return
	}
	req := h.approvals.Get(a.ID, r.PathValue("id"))
	if req == nil || !req.IsAgentRequest() || req.Send.AgentID != c.ID {
		v1Fail(w, id, 404, query.CodeNotFound, "no such send request for this credential", "answered requests are forgotten when wasync restarts")
		return
	}
	view := sendRequestView{RequestID: req.ID, Status: sendRequestStatus(req.Status), Chat: req.Chat, NewNumber: req.Send.NewNumber}
	switch view.Status {
	case "pending":
		exp := req.Expires
		view.ExpiresAt = &exp
		view.ApproveWith = "wasync actions approve " + req.ID
	case "sent":
		view.MessageID = req.Send.MessageID
	}
	v1OK(w, id, view, nil)
}
