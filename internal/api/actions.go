package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gehariharan/wasync/internal/approval"
)

func (h *handler) requestAction(w http.ResponseWriter, r *http.Request) {
	if h.approvals == nil {
		fail(w, 503, "start wasync serve or the TUI for interactive approval")
		return
	}
	var in approval.Input
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		fail(w, 400, "invalid request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, 400, "one request required")
		return
	}
	if err := in.Validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	account := h.account(w, r)
	if account == nil {
		return
	}
	chats, err := h.store.Groups(r.Context(), account.ID)
	if err != nil {
		fail(w, 500, "archive unavailable")
		return
	}
	found := false
	for _, chat := range chats {
		if chat.ID == in.Chat {
			found = true
			break
		}
	}
	if !found {
		fail(w, 404, "chat not in local archive")
		return
	}
	if in.Message != "" {
		page, err := h.store.MessageByWhatsAppID(r.Context(), account.ID, in.Chat, in.Message)
		if err != nil {
			fail(w, 404, "message not in local archive")
			return
		}
		if in.Kind == "revoke-message" && !page.FromMe {
			fail(w, 400, "only your own archived messages can be revoked")
			return
		}
	}
	request, err := h.approvals.Add(account.ID, in)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, request)
}
func (h *handler) actionStatus(w http.ResponseWriter, r *http.Request) {
	if h.approvals == nil {
		fail(w, 503, "approval queue unavailable")
		return
	}
	account := h.account(w, r)
	if account == nil {
		return
	}
	request := h.approvals.Get(account.ID, r.PathValue("id"))
	if request == nil {
		fail(w, 404, "request not found or discarded on restart")
		return
	}
	writeJSON(w, 200, request)
}

func (h *handler) approveAction(w http.ResponseWriter, r *http.Request) {
	if h.approve == nil {
		fail(w, 503, "interactive approval unavailable")
		return
	}
	account := h.account(w, r)
	if account == nil {
		return
	}
	// Access requests carry the human's choice of chat and history depth in the
	// signed body; it is recorded on the pending request just before approval.
	if request := h.approvals.Get(account.ID, r.PathValue("id")); request != nil && request.Kind == approval.KindChatAccess {
		var choice struct {
			Chat    string `json:"chat"`
			History string `json:"history"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&choice); err != nil || decoder.Decode(new(any)) != io.EOF {
			fail(w, 400, "access approval needs {\"chat\":...,\"history\":\"now\"|\"full\"}")
			return
		}
		if err := h.approvals.Decide(account.ID, request.ID, choice.Chat, choice.History); err != nil {
			fail(w, 409, err.Error())
			return
		}
	}
	if err := h.approve(account.ID, r.PathValue("id")); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, h.approvals.Get(account.ID, r.PathValue("id")))
}
