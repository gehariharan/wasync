package api

import (
	"net/http"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func init() {
	registerV1(v1Route{Pattern: "POST /api/v1/messages/{ref}/edit", Scope: agentauth.ScopeEditOwn, Chat: refChat("ref"), Mutation: true, Handle: (*handler).v1Edit})
	registerV1(v1Route{Pattern: "/api/v1/messages/{ref}/edit", Scope: agentauth.ScopeEditOwn, Chat: refChat("ref"), Handle: v1MethodNotAllowed("POST", `use POST /api/v1/messages/{ref}/edit with {"text":"..."}`)})
	query.RegisterFeature("write.edit")
}

// v1Edit replaces the text of one of the account's own messages. wasync
// checks ownership, type and WhatsApp's edit window before sending anything.
func (h *handler) v1Edit(w http.ResponseWriter, r *http.Request, id string) {
	if h.ops.Edit == nil {
		v1WriteOffline(w, id, "editing")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !v1WriteBody(w, r, id, &body) {
		return
	}
	_, m := h.v1Anchor(w, r, id)
	if m == nil {
		return
	}
	in, err := whatsapp.NewEdit(whatsapp.EditRequest{Chat: m.GroupID, MessageID: m.MessageID, Text: body.Text}, *m, time.Now())
	if err != nil {
		v1WriteInvalid(w, id, err)
		return
	}
	result, err := h.ops.Edit(r.Context(), in)
	v1WriteResult(w, id, result, err)
}
