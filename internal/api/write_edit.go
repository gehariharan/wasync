package api

import (
	"net/http"
	"time"

	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (h *handler) edit(w http.ResponseWriter, r *http.Request) {
	if h.ops.Edit == nil {
		fail(w, 503, "editing is unavailable in offline mode")
		return
	}
	var req whatsapp.EditRequest
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
	in, err := whatsapp.NewEdit(req, target, time.Now())
	if err != nil {
		failTarget(w, err)
		return
	}
	result, err := h.ops.Edit(r.Context(), in)
	writeOutcome(w, result, err)
}
