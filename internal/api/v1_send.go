package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// A plain send: a new message to a chat, not a quoted reply. It needs the send
// scope and a chat on the agent's allowlist, exactly like reply, and it goes
// through the same scope check, rate limit and audit record before this handler
// runs. Sending to a chat the agent has not been given is refused; it is never
// turned into a grant by asking.
func init() {
	registerV1(v1Route{Pattern: "POST /api/v1/messages", Scope: agentauth.ScopeSend, Chat: sendBodyChat, Mutation: true, Handle: (*handler).v1Send})
	registerV1(v1Route{Pattern: "/api/v1/messages", Handle: v1MethodNotAllowed("POST", `use POST /api/v1/messages with {"chat":"CHAT_JID","text":"..."}`)})
	query.RegisterFeature("write.send")
}

// sendBodyChat reads the target chat out of the request body so the chat
// allowlist can be enforced before the handler runs. The body is put back for
// the handler, which decodes it strictly.
func sendBodyChat(r *http.Request) string {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 128<<10+1))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return jsonChat(raw)
}

// jsonChat reads the "chat" field leniently; the handler does the strict decode.
func jsonChat(raw []byte) string {
	var in struct {
		Chat string `json:"chat"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	return strings.TrimSpace(in.Chat)
}

func (h *handler) v1Send(w http.ResponseWriter, r *http.Request, id string) {
	if h.sender == nil {
		v1WriteOffline(w, id, "sending")
		return
	}
	var body struct {
		Chat string `json:"chat"`
		Text string `json:"text"`
	}
	if !v1WriteBody(w, r, id, &body) {
		return
	}
	in := whatsapp.SendInput{To: strings.TrimSpace(body.Chat), Text: body.Text}
	if err := in.Validate(); err != nil {
		v1WriteInvalid(w, id, err)
		return
	}
	result, err := h.sender(r.Context(), in)
	h.recordSent(agentName(r), "send", in.To, result, err)
	v1WriteResult(w, id, result, err)
}

// agentName is the authenticated agent's name for the sent log.
func agentName(r *http.Request) string {
	if c := v1Client(r); c != nil && c.Name != "" {
		return c.Name
	}
	return "agent"
}

// recordSent notes a send attempt in the sent log. It is best effort: a log that
// cannot be written never changes what the send reported, because the message may
// already be on its way.
func (h *handler) recordSent(actor, kind, chat string, result whatsapp.SendResult, err error) {
	if h.sent == nil {
		return
	}
	e := sentlog.Entry{Actor: actor, Kind: kind, Chat: chat, MessageID: result.MessageID, Outcome: sentlog.OutcomeSent}
	if err != nil {
		e.Outcome, e.Code = sentlog.OutcomeFailed, query.CodeSendFailed
		if result.MessageID != "" {
			e.Outcome = sentlog.OutcomeUnknown // it left, but nothing came back
		}
	}
	_ = h.sent.Append(e)
}
