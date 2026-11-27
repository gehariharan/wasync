package api

import (
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/firstopen"
	"github.com/gehariharan/wasync/internal/prefs"
	linking "github.com/gehariharan/wasync/internal/setup"
)

// The setup steps walk a first-time person through what the terminal used to,
// in four steps (decision D1): link WhatsApp by QR code and say how wasync
// will be used, choose chats to sync, keep wasync running, and connect an
// agent. Like every page here they need no script and are not a security
// boundary: choices that widen what an agent can do still end in the
// operating system's confirmation prompt (or a passkey, from another device
// on the tailnet). /ui/setup itself leads to the step that is due; once setup
// is done it opens Settings, which holds everything the steps set.

func (h *handler) axiCredentialPath() (string, error) {
	if h.axiPath != nil {
		return h.axiPath()
	}
	return axicred.Path(os.Getenv)
}

// linkState is where linking the phone stands.
type linkState struct {
	Paired, Pairing bool
	QR              template.HTML
	Failure         string
	// Linking is false for an owner that does not link phones (a terminal UI).
	Linking bool
}

func (h *handler) linkState(r *http.Request) (linkState, error) {
	account, err := h.store.Account(r.Context())
	if err != nil {
		return linkState{}, err
	}
	snap := h.setup.Snapshot()
	// While this page links WhatsApp, only the live link counts: an archive
	// kept from an unlinked device must not hide the code to scan again.
	st := linkState{Paired: snap.Paired || (h.setup == nil && account != nil), Failure: snap.Failure, Linking: h.setup != nil}
	if !st.Paired && snap.QR != "" {
		if svg, err := linking.SVG(snap.QR); err == nil {
			st.QR, st.Pairing = template.HTML(svg), true // the SVG is drawn here from numbers, not from page input
		}
	}
	return st, nil
}

// setupRoute leads /ui/setup to the step that is due: the First open screen
// once, then linking until a phone is linked, then the steps until setup is
// done, from choosing chats once step 1 saved how wasync will be used, and
// Settings after that.
func (h *handler) setupRoute(w http.ResponseWriter, r *http.Request, sess uiSession) {
	to := "/ui/settings"
	st, err := h.linkState(r)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	settings, _ := h.prefs.Get()
	switch pending, _ := firstopen.Pending(h.dataDir); {
	case remoteLogin(r) != "":
		// Another device only sets itself up to approve, under Settings.
	case pending:
		to = welcomePath
	case !st.Paired:
		to = "/ui/setup/link"
	case !settings.SetupDone && settings.Use == "":
		to = "/ui/setup/link" // linked: the step asks how wasync will be used
	case !settings.SetupDone:
		to = "/ui/setup/chats"
	}
	if n := r.URL.Query().Get("n"); n != "" {
		to += "?n=" + url.QueryEscape(n)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

type linkData struct {
	uiData
	linkState
	Use string
}

// linkStep is step 1: the QR code to scan, and once linked, how wasync will
// be used ("For my AI agents only" or "Also read my chats here").
func (h *handler) linkStep(w http.ResponseWriter, r *http.Request, sess uiSession) {
	st, err := h.linkState(r)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	d := &linkData{uiData: uiData{CSRF: sess.csrf, Title: "Link your phone · wasync", Setup: h.setupFrameFor(stepLink, st.Paired)}, linkState: st}
	h.noticeFrom(r, &d.uiData)
	if !st.Paired && h.setup != nil && (st.Pairing || st.Failure == "") {
		d.Refresh = 3 // the code changes, and the scan moves this page on
	}
	s, _ := h.prefs.Get()
	switch d.Use = s.Use; d.Use {
	case prefs.UseAgents, prefs.UseChats:
	default:
		d.Use = prefs.UseAgents // the mock's first choice; nothing is saved until Next
	}
	h.webRender(w, r, linkTemplate, d)
}

// setupUse saves how wasync will be used and moves on to choosing chats.
func (h *handler) setupUse(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	use := r.PostForm.Get("use")
	if use != prefs.UseAgents && use != prefs.UseChats {
		http.Redirect(w, r, "/ui/setup/link?n=invalid", http.StatusSeeOther)
		return
	}
	if _, err := h.prefs.Update(func(s *prefs.Settings) { s.Use = use }); err != nil {
		http.Redirect(w, r, "/ui/setup/link?n=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/ui/setup/chats", http.StatusSeeOther)
}

type runningData struct {
	uiData
	Available, On, Remote bool
	Where, Quietly        string
}

// quietly says where wasync waits once its window is closed.
func quietly(where string) string {
	if strings.Contains(where, "Windows") {
		return "Runs quietly in the notification area."
	}
	return "Runs quietly in the menu bar."
}

// runningStep is step 3: start wasync when the person logs in.
func (h *handler) runningStep(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &runningData{uiData: uiData{CSRF: sess.csrf, Title: "Keep running · wasync", Setup: h.setupFrameFor(stepRunning, true)}, Remote: remoteLogin(r) != ""}
	h.noticeFrom(r, &d.uiData)
	if h.service != nil && h.service.Where() != "" {
		d.Available, d.Where = true, h.service.Where()
		d.On, _ = h.service.Installed()
		d.Quietly = quietly(d.Where)
	}
	h.webRender(w, r, runningTemplate, d)
}

// setupFinish marks setup done and opens the app's first page.
func (h *handler) setupFinish(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if _, err := h.prefs.Update(func(s *prefs.Settings) { s.SetupDone = true }); err != nil {
		http.Redirect(w, r, "/ui/setup/agent?n=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, h.homePath(), http.StatusSeeOther)
}

// noticeFrom shows the notice a redirect named.
func (h *handler) noticeFrom(r *http.Request, d *uiData) {
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
}

// backPages are the pages a form may name in its "back" field to return to.
var backPages = map[string]bool{"/ui/settings": true, "/ui/setup/running": true, "/ui/setup/agent": true, "/ui/agents/connect": true}

// formBack is where a setup or settings form returns: the page it names, or Settings.
func formBack(r *http.Request) string {
	if b := r.PostForm.Get("back"); backPages[b] {
		return b
	}
	return "/ui/settings"
}

// setupAgent creates an agent credential and saves it where wasync-axi reads
// it, so the secret is never shown on the page. It needs the system prompt.
func (h *handler) setupAgent(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		h.uiBack(w, r, "unpaired")
		return
	}
	if !h.canConfirm(r) {
		h.uiBack(w, r, noGate(r))
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if agentauth.ValidName(name) != nil {
		h.uiBack(w, r, "badname")
		return
	}
	if existing, err := agentauth.ActiveByName(h.dataDir, name); err != nil {
		h.uiBack(w, r, "failed")
		return
	} else if existing != nil {
		h.uiBack(w, r, "exists")
		return
	}
	path, err := h.axiCredentialPath()
	if err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	if saved, err := axicred.Existing(path); err != nil {
		h.uiBack(w, r, "failed")
		return
	} else if saved != "" && r.PostForm.Get("replace") != "yes" {
		h.uiBack(w, r, "replace")
		return
	}
	// Read, reply and react in every synced chat, now and later, until
	// revoked. Sending anywhere else waits for the person to approve that
	// one message.
	synced, err := syncedChats(r.Context(), h.store, h.dataDir)
	if err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	req := agentauth.ConnectRequest(name, synced)
	if agentauth.ValidateGrant(req) != nil {
		h.uiBack(w, r, "invalid")
		return
	}
	if err := h.confirmError(r.Context(), agentauth.GrantEffect(name, req.Scopes, synced, true, 0)); err != nil {
		h.uiRefused(w, r, err, func(n string) { h.uiBack(w, r, n) })
		return
	}
	credential, client, err := agentauth.Grant(h.dataDir, req, time.Now())
	if err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: "grant", ClientID: client.ID, ClientName: client.Name, Scope: strings.Join(client.Scopes, ","), Outcome: agentauth.OutcomeAllowed})
	if err := axicred.Save(path, credential); err != nil {
		// A credential nobody can read is worse than none: take it back.
		_, _ = agentauth.Revoke(h.dataDir, name, time.Now())
		h.uiBack(w, r, "failed")
		return
	}
	h.uiBack(w, r, "connected")
}

// setupFolder opens the data folder in the file manager of the computer
// running wasync. It is only for the person at that computer: from another
// device it would open a window nobody there sees.
func (h *handler) setupFolder(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if remoteLogin(r) != "" {
		h.uiBack(w, r, "folderhere")
		return
	}
	if err := h.showFolder(h.dataDir); err != nil {
		h.uiBack(w, r, "nofolder")
		return
	}
	h.uiBack(w, r, "")
}

// setupLink asks for a new pairing code after linking stopped.
func (h *handler) setupLink(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	to := "/ui/setup/link"
	if h.setup.Retry() {
		to += "?n=newcode"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// setupService turns start-at-login on or off. Neither asks for the system
// prompt; setLoginItem says why.
func (h *handler) setupService(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) || h.service == nil {
		return
	}
	var on bool
	switch r.PostForm.Get("action") {
	case "install":
		on = true
	case "remove":
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err := h.setLoginItem(r, on); err != nil {
		h.uiRefused(w, r, err, func(n string) { h.uiBack(w, r, n) })
		return
	}
	if on {
		h.uiBack(w, r, "loginon")
	} else {
		h.uiBack(w, r, "loginoff")
	}
}

var linkTemplate = template.Must(template.New("link").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<div class="stage">
{{template "notice" .}}
{{if .Paired}}<p class="linked">✓ Linked.</p>
<h1>How will you use wasync?</h1>
<form method="post" action="/ui/setup/use"><input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="choices">
<label class="choice"><input type="radio" name="use" value="agents"{{if eq .Use "agents"}} checked{{end}}><span><strong>For my AI agents only</strong></span><span class="mut">Your agents can use the chats you approve. You keep reading WhatsApp on your phone.</span></label>
<label class="choice"><input type="radio" name="use" value="chats"{{if eq .Use "chats"}} checked{{end}}><span><strong>Also read my chats here</strong></span><span class="mut">Your synced chats also show up in wasync, with search and replies.</span></label>
</div>
<p class="mut">You can change this later in Settings.</p>
<div class="step-foot"><span></span><button class="go">Next →</button></div>
</form>
{{else}}<h1>Link your phone</h1>
{{if .Pairing}}<div class="link-grid"><div class="qr">{{.QR}}</div>
<div><ol class="howto"><li>Open WhatsApp on your phone</li><li>Settings → Linked devices</li><li>Link a device, then scan this code</li></ol>
<p class="waiting">Waiting for your phone…</p><p class="sub">The code refreshes by itself.</p></div></div>
{{else if .Failure}}<p class="notice bad">{{.Failure}}</p>
<form method="post" action="/ui/setup/link" class="btn-row"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="go">Get a new code</button></form>
{{else if .Linking}}<p class="waiting">Getting a code from WhatsApp…</p>
{{else}}<p class="notice bad">This wasync is not linking a phone. Quit it and open the wasync app to link one.</p>{{end}}
{{end}}
</div>
</main></body></html>
`))

var runningTemplate = template.Must(template.New("running").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<div class="stage">
<h1>Keep wasync running</h1>
{{template "notice" .}}
<p class="lead">wasync syncs your chats and answers your agents only while it runs.</p>
<section class="card">
{{if not .Available}}<p>On this computer wasync runs while its window or terminal is open.</p>
{{else if .Remote}}<p><strong>Start wasync when I log in</strong> is {{if .On}}on{{else}}off{{end}}. Change it on the computer running wasync.</p>
{{else}}<form method="post" action="/ui/setup/service" class="switch-row"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="back" value="/ui/setup/running">
<button class="switch" role="switch" aria-checked="{{.On}}" aria-labelledby="login-label" name="action" value="{{if .On}}remove{{else}}install{{end}}"></button>
<span class="switch-text"><strong id="login-label">Start wasync when I log in</strong><span class="mut">{{.Quietly}}</span></span></form>{{end}}
</section>
<div class="step-foot"><a class="btn ghost" href="/ui/setup/chats">← Back</a><a class="btn go" href="/ui/setup/agent">Next →</a></div>
</div>
</main></body></html>
`))
