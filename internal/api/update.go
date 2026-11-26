package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/update"
)

// Updates are owner-only. The routes live in the web app, so they need a
// signed-in browser session (a one-time link from the approval credential, or
// the system prompt), never an agent credential or the archive token; and
// installing needs the operating system's own confirmation on the computer
// running wasync, as anything else that changes installed software does.
//
//	GET  /ui/update/progress  where updates stand (see update.Status)
//	POST /ui/update/check     ask GitHub for the newest release now
//	POST /ui/update/start     download, check, install and restart (confirmed)
//	GET  /ui/update/result    how the last installed update ended
//
// POST routes need the session's form token as the "csrf" form field or the
// X-Wasync-CSRF header. Nothing checks for updates by itself: each check is a
// request to GitHub the owner asked for.

// Updater is the running owner's update manager (internal/update.Manager).
type Updater interface {
	Status() update.Status
	Check(ctx context.Context) update.Status
	// Start installs version, the one the person confirmed.
	Start(version string) (update.Status, error)
}

const (
	updateProgressPath = "/ui/update/progress"
	updateCheckPath    = "/ui/update/check"
	updateStartPath    = "/ui/update/start"
	updateResultPath   = "/ui/update/result"
	setupUpdatePath    = "/ui/setup/update"
)

// uiDispatchUpdate serves the update routes; it reports whether it did.
func (h *handler) uiDispatchUpdate(w http.ResponseWriter, r *http.Request, sess uiSession) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == updateProgressPath:
		h.updateProgress(w, r)
	case r.Method == http.MethodPost && r.URL.Path == updateCheckPath:
		h.updateCheck(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == updateStartPath:
		h.updateStart(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == updateResultPath:
		h.updateResult(w, r)
	case r.Method == http.MethodPost && r.URL.Path == setupUpdatePath:
		h.setupUpdate(w, r, sess)
	default:
		return false
	}
	return true
}

// uiPostToken checks a JSON route's request-forgery token, sent as a header
// by a page script or as a form field.
func (h *handler) uiPostToken(w http.ResponseWriter, r *http.Request, sess uiSession) bool {
	token := r.Header.Get("X-Wasync-CSRF")
	if token == "" {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		if err := r.ParseForm(); err == nil {
			token = r.PostForm.Get("csrf")
		}
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(sess.csrf)) != 1 {
		fail(w, http.StatusForbidden, "invalid form token")
		return false
	}
	return true
}

func (h *handler) noUpdater(w http.ResponseWriter) bool {
	if h.update == nil {
		fail(w, http.StatusServiceUnavailable, "this wasync cannot check for updates")
		return true
	}
	return false
}

func (h *handler) updateProgress(w http.ResponseWriter, r *http.Request) {
	if h.noUpdater(w) {
		return
	}
	writeJSON(w, http.StatusOK, h.update.Status())
}

func (h *handler) updateResult(w http.ResponseWriter, r *http.Request) {
	if h.noUpdater(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"last_result": h.update.Status().LastResult})
}

func (h *handler) updateCheck(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiPostToken(w, r, sess) || h.noUpdater(w) {
		return
	}
	writeJSON(w, http.StatusOK, h.update.Check(r.Context()))
}

func (h *handler) updateStart(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiPostToken(w, r, sess) || h.noUpdater(w) {
		return
	}
	status, err := h.startUpdate(r)
	if err != nil {
		code := http.StatusConflict
		var se statusError
		if errors.As(err, &se) {
			code = se.code
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "update": status})
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

// startUpdate checks there is an update to install and that the person at
// this computer confirms it, then starts it.
func (h *handler) startUpdate(r *http.Request) (update.Status, error) {
	status := h.update.Status()
	if remoteLogin(r) != "" {
		return status, statusError{code: http.StatusForbidden, msg: "update wasync on the computer running it", notice: "updhere"}
	}
	if !status.CanInstall || status.Latest == nil {
		return status, statusError{code: http.StatusConflict, msg: update.ErrNotReady.Error(), notice: "updnone"}
	}
	if !h.canConfirm(r) {
		return status, statusError{code: http.StatusServiceUnavailable, msg: "this computer cannot show a system confirmation, so wasync cannot update itself from here", notice: "nogate"}
	}
	prompt := "Approve: update wasync from " + humancheck.Clean(status.Current, 60) + " to " + humancheck.Clean(status.Latest.Version, 60) + ". wasync downloads it from GitHub, checks it, installs it and restarts."
	if err := h.confirmError(r.Context(), prompt); err != nil {
		return status, err
	}
	status, err := h.update.Start(status.Latest.Version)
	switch {
	case errors.Is(err, update.ErrBusy):
		return status, statusError{code: http.StatusConflict, msg: err.Error(), notice: "updbusy"}
	case err != nil:
		return status, statusError{code: http.StatusConflict, msg: err.Error(), notice: "updnone"}
	}
	return status, nil
}

// setupUpdate is the setup page's form: check for an update, or install it.
func (h *handler) setupUpdate(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if h.update == nil {
		h.uiBack(w, r, "failed")
		return
	}
	switch r.PostForm.Get("action") {
	case "check":
		h.update.Check(r.Context())
		h.uiBack(w, r, "")
	case "install":
		if _, err := h.startUpdate(r); err != nil {
			h.uiRefused(w, r, err, func(n string) { h.uiBack(w, r, n) })
			return
		}
		h.uiBack(w, r, "updstart")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// uiUpdateSection is the setup page's short, unstyled update notice; the
// setup screens built on the design system replace it.
const uiUpdateSection = `{{define "update"}}{{with .Update}}
<h2 id="update">Updates</h2>
<section class="card">
<p>This is wasync {{.Current}}.{{if .CheckedAt}} <span class="mut">Last checked {{.CheckedAt.Local.Format "2 Jan 15:04"}}.</span>{{end}}</p>
{{with .LastResult}}{{if .OK}}<p class="ok">Updated from {{.From}} to {{.To}}. {{.Restart}}</p>{{else}}<p class="bad">The update to {{.To}} was not installed: {{.Error}} {{.Restart}}</p>{{end}}{{end}}
{{if eq .State "downloading" "verifying" "installing"}}<p role="status"><strong>{{if eq .State "downloading"}}Downloading wasync {{.Latest.Version}}{{with .Progress}}{{if .Total}} ({{.Percent}}%){{end}}{{end}}…{{else if eq .State "verifying"}}Checking the download…{{else}}Installing. wasync restarts in a moment, and this page stops answering until it is back.{{end}}</strong></p>
{{else}}{{if eq .State "up-to-date"}}<p class="ok">You have the newest version.</p>{{else if eq .State "failed"}}<p class="bad">{{.Error}}</p>{{end}}
{{if and .Latest (eq .State "available")}}<p><strong>wasync {{.Latest.Version}} is available.</strong>{{if .Latest.Page}} <a href="{{.Latest.Page}}">What is new</a>{{end}}</p>{{end}}
{{if .Note}}<p class="mut">{{.Note}}</p>{{end}}
<form method="post" action="/ui/setup/update#update"><input type="hidden" name="csrf" value="{{$.CSRF}}">
{{if .CanInstall}}{{if $.Remote}}<p class="mut">Update wasync on the computer running it.</p>{{else if $.Gate}}<button class="go" name="action" value="install">Update and restart…</button> <span class="mut">Asks you to confirm with {{$.Gate}}.</span>{{else}}<p class="bad">This computer cannot show a system confirmation, so wasync cannot update itself from this page.</p>{{end}}{{end}}
<button name="action" value="check">Check for updates</button> <span class="mut">Asks GitHub for the newest release.</span>
</form>{{end}}
</section>{{end}}{{end}}`

// updateBusy reports whether an update is under way, so the page refreshes.
func updateBusy(s *update.Status) bool {
	if s == nil {
		return false
	}
	switch s.State {
	case update.StateChecking, update.StateDownloading, update.StateVerifying, update.StateInstalling:
		return true
	}
	return false
}
