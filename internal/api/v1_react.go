package api

import (
	"net/http"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func init() {
	registerV1(v1Route{Pattern: "PUT /api/v1/messages/{ref}/reaction", Scope: agentauth.ScopeReact, Chat: refChat("ref"), Mutation: true, Handle: (*handler).v1React})
	registerV1(v1Route{Pattern: "DELETE /api/v1/messages/{ref}/reaction", Scope: agentauth.ScopeReact, Chat: refChat("ref"), Mutation: true, Handle: (*handler).v1Unreact})
	registerV1(v1Route{Pattern: "/api/v1/messages/{ref}/reaction", Scope: agentauth.ScopeReact, Chat: refChat("ref"), Handle: v1MethodNotAllowed("PUT, DELETE", `use PUT /api/v1/messages/{ref}/reaction with {"emoji":"👍"}, or DELETE it to clear your reaction`)})
	query.RegisterFeature("write.react")
}

// v1React sets the account's reaction; WhatsApp keeps one per person per message.
func (h *handler) v1React(w http.ResponseWriter, r *http.Request, id string) {
	if h.ops.React == nil {
		v1WriteOffline(w, id, "reacting")
		return
	}
	var body struct {
		Emoji string `json:"emoji"`
	}
	if !v1WriteBody(w, r, id, &body) {
		return
	}
	h.v1Reaction(w, r, id, body.Emoji, false)
}

// v1Unreact clears the account's reaction.
func (h *handler) v1Unreact(w http.ResponseWriter, r *http.Request, id string) {
	if h.ops.React == nil {
		v1WriteOffline(w, id, "reacting")
		return
	}
	h.v1Reaction(w, r, id, "", true)
}

func (h *handler) v1Reaction(w http.ResponseWriter, r *http.Request, id, emoji string, remove bool) {
	_, m := h.v1Anchor(w, r, id)
	if m == nil {
		return
	}
	in, err := whatsapp.NewReact(whatsapp.ReactRequest{Chat: m.GroupID, MessageID: m.MessageID, Emoji: emoji, Remove: remove}, *m)
	if err != nil {
		v1WriteInvalid(w, id, err)
		return
	}
	result, err := h.ops.React(r.Context(), in)
	v1WriteResult(w, id, result, err)
}
