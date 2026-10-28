package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// Just-in-time chat access: an agent asks for a chat it may not use, wasync
// turns the ask into a pending human approval (the same queue, expiry and
// typed confirmation as destructive actions), and only the human's decision
// extends the agent's access. Asking grants nothing.
func init() {
	registerV1(v1Route{Pattern: "POST /api/v1/access-requests", Handle: (*handler).v1RequestAccess})
	registerV1(v1Route{Pattern: "/api/v1/access-requests", Handle: v1MethodNotAllowed("POST", `use POST /api/v1/access-requests with {"chat":"JID"} or {"match":"name or phone"}, optionally "send":true and "edit":true`)})
	registerV1(v1Route{Pattern: "GET /api/v1/access-requests/{id}", Handle: (*handler).v1AccessStatus})
	registerV1(v1Route{Pattern: "/api/v1/access-requests/{id}", Handle: v1MethodNotAllowed("GET, HEAD", "use GET /api/v1/access-requests/{id}")})
	query.RegisterFeature("access.request")
}

type accessRequestBody struct {
	Chat  string `json:"chat"`
	Match string `json:"match"`
	Send  bool   `json:"send"` // also send, reply and react
	Edit  bool   `json:"edit"` // also edit own messages
}

// accessView is the agent-facing state of a request. It never says which chats
// matched, or whether any did: a request that matched nothing looks like any
// other pending request until it is rejected or expires. The chat appears only
// once it was approved.
type accessView struct {
	RequestID   string     `json:"request_id"`
	Status      string     `json:"status"` // pending, approved, rejected, expired, failed or already_allowed
	Access      []string   `json:"access"` // read, plus send and edit when requested
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Chat        string     `json:"chat,omitempty"`    // approved only
	History     string     `json:"history,omitempty"` // approved only: now or full
	ApproveWith string     `json:"approve_with,omitempty"`
}

func accessLevels(send, edit bool) []string {
	levels := []string{"read"}
	if send {
		levels = append(levels, "send")
	}
	if edit {
		levels = append(levels, "edit")
	}
	return levels
}

func agentStatus(status string) string {
	switch status {
	case approval.StatusPending, "executing":
		return "pending"
	case "completed":
		return "approved"
	case "rejected", "expired":
		return status
	}
	return "failed"
}

func (h *handler) v1RequestAccess(w http.ResponseWriter, r *http.Request, id string) {
	c := v1Client(r)
	if h.approvals == nil {
		v1Fail(w, id, 503, query.CodeOwnerOffline, "no wasync owner is running to ask the person", "ask the person to start wasync (the TUI or `wasync serve`); nothing was requested")
		return
	}
	var in accessRequestBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "invalid request body", `send {"chat":"JID"} or {"match":"name or phone"}, optionally "send":true`)
		return
	}
	in.Chat, in.Match = strings.TrimSpace(in.Chat), strings.TrimSpace(in.Match)
	if (in.Chat == "") == (in.Match == "") || len(in.Chat) > 128 || len(in.Match) > 256 {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "give exactly one of chat (a JID) or match (a name or phone number)", "match takes 1 to 256 bytes")
		return
	}
	if err := h.agents.AllowAccessRequest(c); err != nil {
		var denied *agentauth.Error
		if errors.As(err, &denied) {
			v1Fail(w, id, denied.Status, denied.Code, denied.Message, denied.Help)
		} else {
			v1Fail(w, id, 500, query.CodeInternal, "agent registry unavailable", "check permissions of agents.json in the wasync data directory")
		}
		return
	}
	if !c.Restricted() {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "this credential already reads every archived chat", "a chat that is not archived yet must be enabled by the person (wasync chats enable, or the TUI)")
		return
	}
	scopes, err := c.AccessScopes(in.Send, in.Edit)
	if err != nil {
		v1Fail(w, id, 403, query.CodeForbidden, err.Error(), "access requests only use scopes the credential already holds; a human must re-grant it with the scope in an interactive terminal (wasync agents grant)")
		return
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return
	}
	groups, ok := h.v1Groups(w, r, id, a)
	if !ok {
		return
	}
	if in.Chat != "" {
		if _, err = whatsapp.ParseChat(in.Chat); err != nil {
			v1Fail(w, id, 400, query.CodeInvalidArgument, "chat must be an exact chat JID such as 123456789-123@g.us or 15551234567@s.whatsapp.net", "or use match with a name or phone number")
			return
		}
		allowed := true
		for _, s := range scopes {
			allowed = allowed && c.AllowsChatScope(s, in.Chat)
		}
		if allowed {
			writeJSON(w, 200, query.V1Response{RequestID: id, Data: accessView{Status: "already_allowed", Access: accessLevels(in.Send, in.Edit), Chat: in.Chat}})
			return
		}
	}
	candidates, truncated := resolveAccessCandidates(groups, in.Chat, in.Match)
	query_ := in.Chat
	if query_ == "" {
		query_ = in.Match
	}
	mode, _, modeErr := accessmode.Load(h.dataDir)
	if modeErr != nil {
		mode = accessmode.Mode{Mode: accessmode.Ask} // damaged setting: ask each time
	}
	// "Sync all" approves a read, send, reply or react request for the one chat it
	// matched at once; edit access and any ambiguous or empty match wait for the owner.
	auto := mode.AutoApproves() && !in.Edit && len(candidates) == 1 && h.approve != nil
	req, err := h.approvals.AddAccess(a.ID, approval.Access{Agent: c.Name, AgentID: c.ID, Send: in.Send, Edit: in.Edit, Scopes: scopes, Query: query_, Candidates: candidates, Truncated: truncated, Auto: auto})
	if err != nil {
		v1Fail(w, id, 429, query.CodeRateLimited, err.Error(), "wait for the person to answer or for the requests to expire (five minutes)")
		return
	}
	_ = h.agents.Audit(agentauth.AuditEntry{Event: "access_request", ClientID: c.ID, ClientName: c.Name, Scope: strings.Join(scopes, ","), Chat: onlyChat(candidates), Ref: req.ID[:8], Outcome: agentauth.OutcomeAllowed, RequestID: id})
	if auto && req.Status == approval.StatusPending {
		// Still a recorded approval request, decided by the owner's standing choice.
		h.approvals.MarkAuto(a.ID, req.ID)
		if err = h.approvals.Decide(a.ID, req.ID, candidates[0].Chat, mode.History); err == nil {
			err = h.approve(a.ID, req.ID)
		}
		done := h.approvals.Get(a.ID, req.ID)
		view := accessView{RequestID: req.ID, Status: agentStatus(done.Status), Access: accessLevels(in.Send, in.Edit)}
		if view.Status == "approved" {
			view.Chat, view.History = done.Access.Chosen, done.Access.History
		}
		writeJSON(w, http.StatusOK, query.V1Response{RequestID: id, Data: view})
		return
	}
	exp := req.Expires
	writeJSON(w, http.StatusAccepted, query.V1Response{RequestID: id, Data: accessView{RequestID: req.ID, Status: "pending", Access: accessLevels(in.Send, in.Edit), ExpiresAt: &exp, ApproveWith: "wasync actions approve " + req.ID}})
}

func onlyChat(c []approval.Candidate) string {
	if len(c) == 1 {
		return c[0].Chat
	}
	return ""
}

func (h *handler) v1AccessStatus(w http.ResponseWriter, r *http.Request, id string) {
	c := v1Client(r)
	if h.approvals == nil {
		v1Fail(w, id, 503, query.CodeOwnerOffline, "no wasync owner is running", "ask the person to start wasync (the TUI or `wasync serve`)")
		return
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return
	}
	req := h.approvals.Get(a.ID, r.PathValue("id"))
	if req == nil || req.Kind != approval.KindChatAccess || req.Access.AgentID != c.ID {
		v1Fail(w, id, 404, query.CodeNotFound, "no such access request for this credential", "requests are forgotten when wasync restarts; ask again with `whatsapp-axi access request`")
		return
	}
	view := accessView{RequestID: req.ID, Status: agentStatus(req.Status), Access: accessLevels(req.Access.Send, req.Access.Edit)}
	switch view.Status {
	case "pending":
		exp := req.Expires
		view.ExpiresAt = &exp
		view.ApproveWith = "wasync actions approve " + req.ID
	case "approved":
		view.Chat, view.History = req.Access.Chosen, req.Access.History
	}
	v1OK(w, id, view, nil)
}

var phonePattern = regexp.MustCompile(`^\+?[0-9][0-9 ()._-]{5,30}$`)

// phoneDigits returns the digits of a phone-number-looking query, or "".
func phoneDigits(q string) string {
	if !phonePattern.MatchString(q) {
		return ""
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, q)
	if len(digits) < 7 || len(digits) > 15 {
		return ""
	}
	return digits
}

// resolveAccessCandidates turns a JID, phone number or name into concrete chats
// for the human to choose from, searching every archived chat (not just the
// agent's). A phone number or direct JID that is not archived yet still becomes
// a candidate, because direct chats are cached when capture is enabled.
func resolveAccessCandidates(groups []storage.Group, chat, match string) (out []approval.Candidate, truncated bool) {
	add := func(id, name string) {
		for _, c := range out {
			if c.Chat == id {
				return
			}
		}
		if strings.TrimSpace(name) == "" {
			name = id
		}
		out = append(out, approval.Candidate{Chat: id, Name: name})
	}
	byID := map[string]storage.Group{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	switch {
	case chat != "":
		if g, ok := byID[chat]; ok {
			add(g.ID, g.Name)
		} else if storage.ChatKind(chat) == "direct" {
			add(chat, chat)
		}
	case phoneDigits(match) != "":
		user := phoneDigits(match)
		for _, g := range groups {
			if storage.ChatKind(g.ID) == "direct" && strings.SplitN(g.ID, "@", 2)[0] == user {
				add(g.ID, g.Name)
			}
		}
		add(user+"@s.whatsapp.net", "")
	default:
		for _, g := range query.MatchChats(groups, match) {
			add(g.ID, g.Name)
		}
	}
	if len(out) > 10 {
		return out[:10], true
	}
	return out, false
}
