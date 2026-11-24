package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	if h.sender == nil {
		fail(w, 503, "sending is unavailable in offline mode")
		return
	}
	var in whatsapp.SendInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		fail(w, 400, "invalid send request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, 400, "one JSON object required")
		return
	}
	if err := in.Validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	result, err := h.sender(r.Context(), in)
	h.recordSent(sentlog.Owner, "send", in.To, result, err)
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{"result": result, "error": errorText(err)})
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
