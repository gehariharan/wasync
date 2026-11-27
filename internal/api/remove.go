package api

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/uninstall"
)

// Remove wasync, from Settings, with no terminal. On this computer only:
//
//  1. The person confirms with the computer's own check first, once for the
//     whole removal, including deleting the data when "Also delete my chats
//     and data" is ticked (it is not by default); declining changes nothing
//     at all.
//  2. Every app wasync connected is disconnected (wasync's entry comes out of
//     its settings and any entry it replaced goes back), and every other agent
//     credential is revoked.
//  3. The phone is unlinked from WhatsApp.
//  4. If anything in 2 or 3 could not be done, the page says what and how to
//     finish it by hand, and waits for "Finish removing".
//  5. wasync starts the removal helper (internal/uninstall) and stops. Once
//     wasync and the wasync app have stopped, the helper removes the
//     start-at-login items, deletes the data if asked, and hands over to the
//     platform's own uninstall: Windows Setup's uninstaller, or moving
//     wasync.app to the Bin.

const removePath = "/ui/settings/remove"

// removeWait is how long a confirmed removal waits for "Finish removing".
const removeWait = 10 * time.Minute

// Remover starts the removal helper. The zero value uses the running program
// and the real helper; tests replace both.
type Remover struct {
	// Handoff says how the program files are removed.
	Handoff func() uninstall.Handoff
	// Start records the removal and starts the helper.
	Start func(uninstall.Request) error
}

// removal is a removal the person confirmed whose page is waiting for
// "Finish removing". It belongs to the session that confirmed it.
type removal struct {
	mu         sync.Mutex
	csrf       string
	deleteData bool
	at         time.Time
}

func (h *handler) handoff() uninstall.Handoff {
	if h.remover.Handoff != nil {
		return h.remover.Handoff()
	}
	exe, err := os.Executable()
	if err != nil {
		return uninstall.Handoff{Kind: uninstall.KindManual, Note: "Remove the wasync program the way you installed it."}
	}
	return uninstall.DetectHandoff(exe)
}

func (h *handler) startRemoval(req uninstall.Request) error {
	if h.remover.Start != nil {
		return h.remover.Start(req)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return uninstall.Start(exe, req)
}

type removeData struct {
	uiData
	Agents     int
	Apps       []string
	LoginItem  bool
	Linked     bool
	DataDir    string
	Handoff    uninstall.Handoff
	CanConfirm bool
	DeleteData bool
	// Problems are what could not be done; Finishing is the page that waits
	// for "Finish removing"; Gone is the last page, shown as wasync stops.
	Problems  []string
	Finishing bool
	Gone      bool
}

func (h *handler) removeData(r *http.Request, sess uiSession) (*removeData, error) {
	d := &removeData{uiData: uiData{CSRF: sess.csrf, Gate: h.confirmName(r), Title: "Remove wasync", Remote: remoteLogin(r) != ""}, DataDir: h.dataDir, Handoff: h.handoff()}
	h.noticeFrom(r, &d.uiData)
	d.CanConfirm = !d.Remote && h.human.Enabled()
	account, err := h.store.Account(r.Context())
	if err != nil {
		return nil, err
	}
	d.Linked = account != nil
	conns, _ := agentconnect.Connections(h.dataDir)
	for _, c := range conns {
		d.Apps = append(d.Apps, agentTitle(string(c.Agent), c.Client))
	}
	if clients, err := agentauth.List(h.dataDir); err == nil {
		now := time.Now()
		for _, c := range clients {
			if c.Status(now) == "active" {
				d.Agents++
			}
		}
	}
	if h.service != nil && h.service.Where() != "" {
		d.LoginItem, _ = h.service.Installed()
	}
	return d, nil
}

func (h *handler) removePage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d, err := h.removeData(r, sess)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	h.webRender(w, r, removeTemplate, d)
}

func removeBack(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, removePath+"?n="+notice, http.StatusSeeOther)
}

// removeAct runs the removal: action=remove starts it, action=finish carries
// on after the page listed what could not be done.
func (h *handler) removeAct(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if remoteLogin(r) != "" {
		removeBack(w, r, "removehere")
		return
	}
	d, err := h.removeData(r, sess)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	switch r.PostForm.Get("action") {
	case "remove":
	case "finish":
		h.removal.mu.Lock()
		ok := h.removal.csrf != "" && h.removal.csrf == sess.csrf && time.Since(h.removal.at) < removeWait
		if ok {
			d.DeleteData = h.removal.deleteData
			h.removal.csrf = "" // finishes once
		}
		h.removal.mu.Unlock()
		if !ok {
			removeBack(w, r, "removeexpired")
			return
		}
		h.finishRemoval(w, r, d)
		return
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	d.DeleteData = r.PostForm.Get("delete") == "yes"
	if !d.CanConfirm {
		removeBack(w, r, "nogate")
		return
	}
	prompt := "Approve: remove wasync from this computer, revoking every agent's credential and unlinking WhatsApp; your data is kept in " + h.dataDir + "."
	if d.DeleteData {
		prompt = "Approve: remove wasync from this computer, revoking every agent's credential, and delete all its data, which cannot be undone: your synced chats, saved files, settings and the link to WhatsApp, in " + h.dataDir + "."
	}
	if err := h.confirmError(r.Context(), prompt); err != nil {
		removeBack(w, r, noticeFor(err))
		return
	}
	d.Problems = h.takeAwayAccess(r.Context())
	if len(d.Problems) > 0 {
		h.removal.mu.Lock()
		h.removal.csrf, h.removal.deleteData, h.removal.at = sess.csrf, d.DeleteData, time.Now()
		h.removal.mu.Unlock()
		d.Finishing = true
		h.webRender(w, r, removeTemplate, d)
		return
	}
	h.finishRemoval(w, r, d)
}

// takeAwayAccess disconnects every app wasync connected, revokes every other
// agent and unlinks the phone. It returns, in plain words, what it could not
// do and how to finish that by hand.
func (h *handler) takeAwayAccess(ctx context.Context) []string {
	var problems []string
	h.connectMu.Lock()
	conns, err := agentconnect.Connections(h.dataDir)
	if err != nil {
		problems = append(problems, "wasync could not read which apps it connected. Check your AI apps' settings for a \"whatsapp\" tool server and remove it.")
	}
	for _, c := range conns {
		title := agentTitle(string(c.Agent), c.Client)
		switch h.disconnect(ctx, c) {
		case "disconnectleft", "failed":
			problems = append(problems, "wasync could not take its entry out of "+title+"'s settings ("+c.ConfigPath+"). Remove the \"whatsapp\" tool server there; a copy from before wasync changed it is at "+backupOr(c.Backup)+".")
		case "pluginleft":
			problems = append(problems, "The WhatsApp plugin could not be removed from Codex. Remove it in Codex if you no longer want it.")
		}
	}
	h.connectMu.Unlock()
	now := time.Now()
	if clients, err := agentauth.List(h.dataDir); err == nil {
		for _, c := range clients {
			if c.Status(now) != "active" {
				continue
			}
			if _, err := agentauth.Revoke(h.dataDir, c.Name, now); err == nil {
				_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: "revoke", ClientID: c.ID, ClientName: c.Name, Outcome: agentauth.OutcomeAllowed})
			}
		}
	}
	linked := h.paired != nil && h.paired()
	if account, err := h.store.Account(ctx); err == nil && account != nil && h.paired == nil {
		linked = true // no live connection to ask: assume the phone still lists it
	}
	if linked {
		err := errors.New("no WhatsApp connection")
		if h.unlink != nil {
			uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = h.unlink(uctx)
			cancel()
		}
		if err != nil && !errors.Is(err, ErrNotLinked) {
			problems = append(problems, "wasync could not unlink this computer from WhatsApp. On your phone, open WhatsApp, Settings, Linked devices, and log out this computer.")
		}
	}
	return problems
}

func backupOr(path string) string {
	if path == "" {
		return "nowhere: wasync created that file"
	}
	return path
}

// finishRemoval starts the helper and stops wasync once the last page is out.
func (h *handler) finishRemoval(w http.ResponseWriter, r *http.Request, d *removeData) {
	dir, err := filepath.Abs(h.dataDir)
	if err != nil {
		removeBack(w, r, "failed")
		return
	}
	req := uninstall.Request{DataDir: dir, DeleteData: d.DeleteData, Handoff: d.Handoff}
	if h.info.StartedBy == appStarted {
		req.AppPID = os.Getppid() // the wasync app started this wasync; the helper waits for it to quit
	}
	if err := h.startRemoval(req); err != nil {
		removeBack(w, r, "removefail")
		return
	}
	d.Gone, d.Finishing = true, false
	h.webRender(w, r, removeTemplate, d)
	if h.stop != nil {
		// Let the page reach the window before wasync, and the app, stop.
		time.AfterFunc(2*time.Second, h.stop)
	}
}

func init() {
	for k, v := range map[string]struct {
		text string
		ok   bool
	}{
		"removehere":    {"Remove wasync on the computer running it.", false},
		"removeexpired": {"That page waited too long, so nothing more was removed. Start again.", false},
		"removefail":    {"wasync could not start removing itself, so it keeps running. Your agents stay disconnected.", false},
	} {
		notices[k] = v
	}
}

var removeTemplate = template.Must(template.New("remove").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<h1>Remove wasync</h1>
{{template "notice" .}}
{{if .Gone}}<section class="card">
<p class="waiting" role="status">Removing wasync…</p>
<p>wasync stops now{{if eq .Handoff.Kind "mac-app"}}, then moves itself to the Bin{{else if eq .Handoff.Kind "windows-setup"}}, then Windows asks you to confirm removing wasync{{end}}.{{if .DeleteData}} Your chats and data are deleted.{{else}} Your chats and data are kept in <code>{{.DataDir}}</code>.{{end}}</p>
{{if eq .Handoff.Kind "manual"}}<p>{{.Handoff.Note}}</p>{{end}}
{{range .Problems}}<p class="notice warn">{{.}}</p>{{end}}
<p class="mut">You can close this window.</p>
</section>
{{else if .Finishing}}<section class="card">
<p>Your agents are disconnected. A few things need you before wasync goes:</p>
{{range .Problems}}<p class="notice warn">{{.}}</p>{{end}}
<form method="post" action="/ui/settings/remove" class="btn-row"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="danger" name="action" value="finish">Finish removing</button> <a class="btn ghost" href="/ui/settings">Keep wasync</a></form>
</section>
{{else if .Remote}}<p class="notice bad">Remove wasync on the computer running it.</p>
{{else if not .CanConfirm}}<p class="notice bad">This computer cannot show a system confirmation, so wasync cannot be removed on this page. {{.Handoff.Note}}</p>
{{else}}<section class="card">
<p>wasync will:</p>
<ol class="remove-steps">
<li>Disconnect {{if .Apps}}{{range $i, $a := .Apps}}{{if $i}}, {{end}}{{$a}}{{end}}{{else if .Agents}}your agents{{else}}any agent{{end}}, and take wasync out of each app's settings.</li>
<li>Stop starting when you log in.</li>
<li>{{if .Linked}}Unlink this computer from WhatsApp on your phone.{{else}}No phone is linked, so there is nothing to unlink.{{end}}</li>
<li>{{if eq .Handoff.Kind "mac-app"}}Quit and move wasync to the Bin.{{else if eq .Handoff.Kind "windows-setup"}}Open the Windows uninstaller, the same one Settings, Apps runs.{{else}}Stop. {{.Handoff.Note}}{{end}}</li>
</ol>
<form method="post" action="/ui/settings/remove"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label class="choice risky"><input type="checkbox" name="delete" value="yes"><span><strong>Also delete my chats and data</strong></span><span class="mut">Deletes <code>{{.DataDir}}</code> and the keys wasync keeps. This cannot be undone. Unticked, your data stays for a later install.</span></label>
<p class="mut">You confirm the removal with {{.Gate}} first.</p>
<div class="step-foot"><a class="btn ghost" href="/ui/settings">Cancel</a><button class="danger" name="action" value="remove">Remove wasync</button></div>
</form>
</section>{{end}}
</main></body></html>
`))
