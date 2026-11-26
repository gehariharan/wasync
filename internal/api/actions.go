package api

import (
	"context"
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

// listActions returns the account's pending requests for `wasync actions list`.
func (h *handler) listActions(w http.ResponseWriter, r *http.Request) {
	if h.approvals == nil {
		fail(w, 503, "approval queue unavailable")
		return
	}
	account := h.account(w, r)
	if account == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"items": h.approvals.PendingList(account.ID)})
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
	if err := h.approveRequest(r.Context(), account.ID, r.PathValue("id")); err != nil {
		failWith(w, err)
		return
	}
	writeJSON(w, 200, h.approvals.Get(account.ID, r.PathValue("id")))
}

// approveRequest is the one way a person's approval reaches the owner, whether
// it came from the command line or the approval page. It makes the person
// confirm the exact effect using the operating system's prompt (when this
// computer has one), and only then runs the action.
func (h *handler) approveRequest(ctx context.Context, account int64, id string) error {
	if request := h.approvals.Get(account, id); request != nil && request.Status == approval.StatusPending {
		if err := h.confirmError(ctx, request.Prompt(h.chatName(ctx, account, request.Chat))); err != nil {
			return err
		}
	}
	if err := h.approve(account, id); err != nil {
		return statusError{code: http.StatusConflict, msg: err.Error()}
	}
	return nil
}
