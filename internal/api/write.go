package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// isWritePath reports the signed legacy routes that talk to WhatsApp and so
// run without the ordinary short command deadline.
func isWritePath(path string) bool {
	return path == "/api/send" || path == "/api/reply" || path == "/api/react" || path == "/api/edit"
}

// writeTarget resolves chat and message ID to an archived message. Writes
// only ever act on messages wasync has archived, so a disabled chat or an
// expired message cannot be targeted.
func (h *handler) writeTarget(w http.ResponseWriter, r *http.Request, chat, messageID string) (storage.Message, bool) {
	a := h.account(w, r)
	if a == nil {
		return storage.Message{}, false
	}
	m, err := h.store.MessageByRef(r.Context(), a.ID, a.JID, msgref.Ref{Account: a.JID, Chat: chat, Message: messageID})
	if errors.Is(err, storage.ErrMessageNotFound) {
		fail(w, 404, "message is not in this account's archive")
		return storage.Message{}, false
	}
	if err != nil {
		fail(w, 500, "archive unavailable")
		return storage.Message{}, false
	}
	return m, true
}

// decodeWrite reads one strict JSON object, as send does.
func decodeWrite(w http.ResponseWriter, r *http.Request, v any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		fail(w, 400, "invalid request")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, 400, "one JSON object required")
		return false
	}
	return true
}

// failTarget maps a target the operation cannot act on to an error response.
func failTarget(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, whatsapp.ErrTargetUnavailable), errors.Is(err, whatsapp.ErrNotOwnMessage), errors.Is(err, whatsapp.ErrNotEditable), errors.Is(err, whatsapp.ErrEditWindowExpired):
		fail(w, 409, err.Error())
	default:
		fail(w, 400, err.Error())
	}
}

// writeOutcome answers like send: 200 with the result, 502 when WhatsApp did not acknowledge.
func writeOutcome(w http.ResponseWriter, result whatsapp.SendResult, err error) {
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{"result": result, "error": errorText(err)})
}

func (h *handler) reply(w http.ResponseWriter, r *http.Request) {
	if h.ops.Reply == nil {
		fail(w, 503, "replying is unavailable in offline mode")
		return
	}
	var req whatsapp.ReplyRequest
	if !decodeWrite(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	target, ok := h.writeTarget(w, r, req.Chat, req.MessageID)
	if !ok {
		return
	}
	in, err := whatsapp.NewReply(req, target)
	if err != nil {
		failTarget(w, err)
		return
	}
	result, err := h.ops.Reply(r.Context(), in)
	writeOutcome(w, result, err)
}
