package api

import (
	"net/http"
	"time"

	"github.com/gehariharan/wasync/internal/firstopen"
)

// Two settings of the wasync app, for its setup screens to read and change
// with a script on a signed-in page: whether the First open screen is still to
// be shown, and "Start when I log in". They answer with JSON, on this computer
// only: they are about this computer, so a device signed in through the
// tailnet gets 403. Changes are forms carrying the page's csrf field, like
// every other change on these pages.

const (
	desktopPath          = "/ui/desktop"
	desktopFirstOpenPath = "/ui/desktop/first-open"
	desktopLoginPath     = "/ui/desktop/start-at-login"
)

// loginItemPrompt is what the system prompt says before wasync starts at login.
const loginItemPrompt = "Approve: start wasync by itself every time you log in, and keep it running in the background"

type desktopLoginItem struct {
	// Available is false where this computer has no login item for wasync (Linux).
	Available bool   `json:"available"`
	On        bool   `json:"on"`
	Where     string `json:"where,omitempty"`
	// Confirm names the system prompt that turning it on asks for; empty when
	// this computer cannot show one, and then it cannot be turned on here.
	Confirm string `json:"confirm,omitempty"`
}

type desktopSettings struct {
	FirstOpen    bool             `json:"first_open"`
	StartAtLogin desktopLoginItem `json:"start_at_login"`
}

func (h *handler) desktopSettings(r *http.Request) (desktopSettings, error) {
	pending, err := firstopen.Pending(h.dataDir)
	if err != nil {
		return desktopSettings{}, err
	}
	d := desktopSettings{FirstOpen: pending}
	if h.service != nil && h.service.Where() != "" {
		on, err := h.service.Installed()
		if err != nil {
			return desktopSettings{}, err
		}
		d.StartAtLogin = desktopLoginItem{Available: true, On: on, Where: h.service.Where(), Confirm: h.confirmName(r)}
	}
	return d, nil
}

// desktopRoute answers the three paths; ok is false for any other request.
func (h *handler) desktopRoute(w http.ResponseWriter, r *http.Request, sess uiSession) (ok bool) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == desktopPath:
	case r.Method == http.MethodPost && (r.URL.Path == desktopFirstOpenPath || r.URL.Path == desktopLoginPath):
	default:
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if remoteLogin(r) != "" {
		fail(w, http.StatusForbidden, "this setting is about the computer running wasync; change it there")
		return true
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		if !h.uiForm(w, r, sess) {
			return true
		}
	}
	switch r.URL.Path {
	case desktopFirstOpenPath:
		if err := firstopen.Done(h.dataDir, time.Now()); err != nil {
			fail(w, http.StatusInternalServerError, "could not save that the First open screen was seen")
			return true
		}
	case desktopLoginPath:
		var on bool
		switch r.PostForm.Get("on") {
		case "true":
			on = true
		case "false":
		default:
			fail(w, http.StatusBadRequest, `on must be "true" or "false"`)
			return true
		}
		if err := h.setLoginItem(r, on); err != nil {
			failWith(w, err)
			return true
		}
	}
	d, err := h.desktopSettings(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "settings unavailable")
		return true
	}
	writeJSON(w, http.StatusOK, d)
	return true
}

// setLoginItem turns start-at-login on or off. Turning it on asks for the
// system prompt first, because it leaves a program that runs at every login;
// turning it off never does. Both the setup page's button and the JSON setting
// come here.
func (h *handler) setLoginItem(r *http.Request, on bool) error {
	if h.service == nil || h.service.Where() == "" {
		return statusError{code: http.StatusNotImplemented, msg: "this computer has no login item for wasync", notice: "failed"}
	}
	if !on {
		if err := h.service.Uninstall(); err != nil {
			return statusError{code: http.StatusInternalServerError, msg: "could not remove the login item", notice: "failed"}
		}
		return nil
	}
	if !h.canConfirm(r) {
		return statusError{code: http.StatusForbidden, msg: "this computer cannot show a system confirmation, so start-at-login cannot be turned on here", notice: noGate(r)}
	}
	if err := h.confirmError(r.Context(), loginItemPrompt); err != nil {
		return err
	}
	if err := h.service.Install(h.serviceSpec); err != nil {
		return statusError{code: http.StatusInternalServerError, msg: "could not add the login item", notice: "failed"}
	}
	return nil
}
