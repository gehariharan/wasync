package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// W-06/A-07: writes beyond send. Each operation has its own scope; wasync
// enforces scope and chat allowlist before any handler runs, and a write only
// ever targets a message in the current account's archive.
func init() {
	registerV1(v1Route{Pattern: "POST /api/v1/messages/{ref}/reply", Scope: agentauth.ScopeSend, Chat: refChat("ref"), Mutation: true, Handle: (*handler).v1Reply})
	registerV1(v1Route{Pattern: "/api/v1/messages/{ref}/reply", Scope: agentauth.ScopeSend, Chat: refChat("ref"), Handle: v1MethodNotAllowed("POST", `use POST /api/v1/messages/{ref}/reply with {"text":"...","mentions":[]}`)})
	query.RegisterFeature("write.reply")
}

// v1WriteBody reads one strict JSON object of at most 128 KiB.
func v1WriteBody(w http.ResponseWriter, r *http.Request, id string, v any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "invalid JSON request body", "send one JSON object with the documented fields")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "one JSON object required", "")
		return false
	}
	return true
}

// v1WriteInvalid maps a target or argument problem found before sending.
func v1WriteInvalid(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, whatsapp.ErrTargetUnavailable) {
		v1Fail(w, id, 409, query.CodeTargetUnavailable, err.Error(), "choose a message that is still present in the conversation")
		return
	}
	v1Fail(w, id, 400, query.CodeInvalidArgument, err.Error(), "")
}

// v1WriteOffline answers when the owner has no connected WhatsApp client.
func v1WriteOffline(w http.ResponseWriter, id, action string) {
	v1Fail(w, id, 503, query.CodeOwnerOffline, action+" needs wasync running and connected to WhatsApp", "start wasync tui or wasync serve with a paired session")
}

// v1WriteResult reports a WhatsApp acknowledgement, or an unknown outcome as
// send_failed; an agent must never retry that automatically.
func v1WriteResult(w http.ResponseWriter, id string, result whatsapp.SendResult, err error) {
	if err != nil {
		v1Fail(w, id, 502, query.CodeSendFailed, err.Error(), "check WhatsApp before retrying; do not retry automatically")
		return
	}
	v1OK(w, id, result, nil)
}

func (h *handler) v1Reply(w http.ResponseWriter, r *http.Request, id string) {
	if h.ops.Reply == nil {
		v1WriteOffline(w, id, "replying")
		return
	}
	var body struct {
		Text     string   `json:"text"`
		Mentions []string `json:"mentions"`
	}
	if !v1WriteBody(w, r, id, &body) {
		return
	}
	_, m := h.v1Anchor(w, r, id)
	if m == nil {
		return
	}
	in, err := whatsapp.NewReply(whatsapp.ReplyRequest{Chat: m.GroupID, MessageID: m.MessageID, Text: body.Text, Mentions: body.Mentions}, *m)
	if err != nil {
		v1WriteInvalid(w, id, err)
		return
	}
	result, err := h.ops.Reply(r.Context(), in)
	v1WriteResult(w, id, result, err)
}
