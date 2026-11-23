package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// ErrNotLinked is what an owner's Unlink returns when there is no linked
// device to remove.
var ErrNotLinked = errors.New("no saved session to unlink")

// OwnerInfo says which wasync this is, so a command can show the person what
// it reached and check it reached the one it meant to.
type OwnerInfo struct {
	// Kind is the command that started it: serve, tui, run, setup or login.
	Kind      string    `json:"kind"`
	PID       int       `json:"pid"`
	DataDir   string    `json:"data_dir"`
	StartedAt time.Time `json:"started_at"`
	// StartedBy is "login item" or "manually" when known.
	StartedBy string `json:"started_by,omitempty"`
}

type ownerRequest struct {
	PID int `json:"pid"`
}

// ownerRequestFor reads the optional body naming the process the caller meant.
// A mismatch means the address now belongs to a different wasync.
func (h *handler) ownerRequestFor(w http.ResponseWriter, r *http.Request) bool {
	var in ownerRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		fail(w, 400, "invalid request")
		return false
	}
	if len(body) > 0 && json.Unmarshal(body, &in) != nil {
		fail(w, 400, "invalid request")
		return false
	}
	if in.PID != 0 && in.PID != h.info.PID {
		fail(w, 409, "this address belongs to a different wasync than the one you meant")
		return false
	}
	return true
}

// getOwner says who this wasync is and what state it is in.
func (h *handler) getOwner(w http.ResponseWriter, r *http.Request) {
	connection := "offline"
	if h.status != nil {
		connection = h.status().Connection
	}
	out := map[string]any{"owner": h.info, "connection": connection, "can_unlink": h.unlink != nil}
	if h.paired != nil {
		out["paired"] = h.paired()
	}
	writeJSON(w, 200, out)
}

// stopOwner shuts the running wasync down cleanly, as quitting it would.
func (h *handler) stopOwner(w http.ResponseWriter, r *http.Request) {
	if h.stop == nil {
		fail(w, 503, "this wasync cannot be stopped from here")
		return
	}
	if !h.ownerRequestFor(w, r) {
		return
	}
	writeJSON(w, 202, map[string]any{"stopping": true, "owner": h.info})
	go h.stop()
}

// logoutOwner unlinks the device through the live connection, then shuts the
// owner down: with no linked phone it has nothing left to capture, and
// stopping frees the data directory for pairing a new one.
func (h *handler) logoutOwner(w http.ResponseWriter, r *http.Request) {
	if h.unlink == nil {
		fail(w, 503, "this wasync has no WhatsApp connection (it was started offline), so there is nothing to unlink")
		return
	}
	if !h.ownerRequestFor(w, r) {
		return
	}
	err := h.unlink(r.Context())
	switch {
	case errors.Is(err, ErrNotLinked):
		writeJSON(w, 409, map[string]any{"error": err.Error(), "unlinked": false, "owner": h.info})
		return
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeJSON(w, 504, map[string]any{"error": "unlinking took too long; the device may still be linked", "unlinked": false, "owner": h.info})
		return
	case err != nil:
		writeJSON(w, 502, map[string]any{"error": err.Error(), "unlinked": false, "owner": h.info})
		return
	}
	writeJSON(w, 200, map[string]any{"unlinked": true, "stopping": h.stop != nil, "owner": h.info})
	if h.stop != nil {
		go h.stop()
	}
}
