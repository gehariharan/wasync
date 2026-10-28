package api

import (
	"net/http"

	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (h *handler) react(w http.ResponseWriter, r *http.Request) {
	if h.ops.React == nil {
		fail(w, 503, "reacting is unavailable in offline mode")
		return
	}
	var req whatsapp.ReactRequest
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
	in, err := whatsapp.NewReact(req, target)
	if err != nil {
		failTarget(w, err)
		return
	}
	result, err := h.ops.React(r.Context(), in)
	writeOutcome(w, result, err)
}
