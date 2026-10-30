package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gehariharan/wasync/internal/humancheck"
)

// statusError is a failure with the HTTP status it should be reported with.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

func failWith(w http.ResponseWriter, err error) {
	var se statusError
	if errors.As(err, &se) {
		fail(w, se.code, se.msg)
		return
	}
	fail(w, http.StatusInternalServerError, err.Error())
}

// confirmError makes the person at this computer approve prompt with the
// operating system's own check. A signed request is not enough on its own: the
// signing credential is a file the agent's user can read, while the prompt can
// only be answered by a person. When this computer has no such prompt (Linux, a
// session with no screen), the typed confirmation in the terminal stays the
// only check; the owner says so when it starts.
func (h *handler) confirmError(ctx context.Context, prompt string) error {
	if !h.human.Enabled() {
		return nil
	}
	err := h.human.Confirm(ctx, prompt)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, humancheck.ErrBusy), errors.Is(err, humancheck.ErrLocked):
		return statusError{http.StatusTooManyRequests, err.Error() + "; nothing changed"}
	}
	return statusError{http.StatusForbidden, "not confirmed on this computer; nothing changed"}
}

func (h *handler) confirm(w http.ResponseWriter, r *http.Request, prompt string) bool {
	if err := h.confirmError(r.Context(), prompt); err != nil {
		failWith(w, err)
		return false
	}
	return true
}

// chatName is the archive's name for a chat, or "" when it has none.
func (h *handler) chatName(ctx context.Context, account int64, chat string) string {
	groups, err := h.store.Groups(ctx, account)
	if err != nil {
		return ""
	}
	for _, g := range groups {
		if g.ID == chat {
			return g.Name
		}
	}
	return ""
}
