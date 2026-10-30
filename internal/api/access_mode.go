package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gehariharan/wasync/internal/accessmode"
)

const accessModePath = "/api/access/mode"

// The access mode is a human setting, so these routes use the approve
// credential like interactive approval does: no agent credential, query token
// or send token reaches them.

func (h *handler) getAccessMode(w http.ResponseWriter, r *http.Request) {
	m, set, err := accessmode.Load(h.dataDir)
	if err != nil {
		fail(w, 500, "access mode unreadable; treated as ask each time")
		return
	}
	writeJSON(w, 200, map[string]any{"mode": m.Mode, "history": m.History, "chosen": set, "set_at": m.SetAt})
}

func (h *handler) setAccessMode(w http.ResponseWriter, r *http.Request) {
	var in accessmode.Mode
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.Decode(new(any)) != io.EOF {
		fail(w, 400, `send {"mode":"ask"} or {"mode":"all","history":"now"|"full"}`)
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
	if in.Mode == accessmode.All && !h.confirm(w, r, "Approve: "+in.Effect()) {
		return
	}
	enabled, err := accessmode.Apply(r.Context(), h.store, h.dataDir, account.ID, in)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"mode": in.Mode, "history": in.History, "chats_enabled": enabled})
}
