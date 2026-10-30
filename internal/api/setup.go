package api

import (
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/humancheck"
	linking "github.com/gehariharan/wasync/internal/setup"
	"github.com/gehariharan/wasync/internal/storage"
)

// The setup page walks a first-time person through what the terminal used to:
// link WhatsApp by QR code, choose chats, connect an agent, and start wasync
// at login. Like the approval page it has no script and is not a security
// boundary. Choices that widen what an agent can do or start a background
// program at login still end in the operating system's confirmation prompt, and
// where the computer has none the page only shows the command to run instead.

const maxSetupChats = 300

type setupChat struct {
	ID, Name, Kind string
	Enabled        bool
}

type setupAgent struct{ Name, Status string }

type setupData struct {
	uiData
	Paired, Pairing   bool
	QR                template.HTML
	Failure           string
	Chats             []setupChat
	MoreChats         int
	Agents            []setupAgent
	AxiPath           string
	HasSavedCred      bool
	Service           string
	ServiceInstalled  bool
	ServiceAvailable  bool
	ServiceNeedsHuman bool
}

func (h *handler) axiCredentialPath() (string, error) {
	if h.axiPath != nil {
		return h.axiPath()
	}
	return axicred.Path(os.Getenv)
}

func (h *handler) setupHome(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &setupData{uiData: uiData{CSRF: sess.csrf, Gate: h.human.Name(), Title: "wasync setup"}}
	if !h.human.Enabled() {
		d.Gate = ""
	}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	snap := h.setup.Snapshot()
	// While this page links WhatsApp, only the live link counts: an archive
	// kept from an unlinked device must not hide the code to scan again.
	d.Paired = snap.Paired || (h.setup == nil && account != nil)
	d.Failure = snap.Failure
	if !d.Paired && snap.QR != "" {
		if svg, err := linking.SVG(snap.QR); err == nil {
			d.QR, d.Pairing = template.HTML(svg), true // the SVG is drawn here from numbers, not from page input
			d.Refresh = 3
		}
	}
	if !d.Paired && !d.Pairing && h.setup != nil && d.Failure == "" {
		d.Refresh = 3 // waiting for WhatsApp to offer the first code
	}
	if account != nil {
		groups, err := h.store.GroupSummaries(r.Context(), account.ID)
		if err != nil {
			http.Error(w, "archive unavailable", http.StatusInternalServerError)
			return
		}
		for _, g := range groups {
			if len(d.Chats) == maxSetupChats {
				d.MoreChats++
				continue
			}
			d.Chats = append(d.Chats, setupChat{ID: g.ID, Name: humancheck.Clean(g.Name, 80), Kind: storage.ChatKind(g.ID), Enabled: g.Enabled})
		}
		clients, _ := agentauth.List(h.dataDir)
		now := time.Now()
		for _, c := range clients {
			d.Agents = append(d.Agents, setupAgent{Name: humancheck.Clean(c.Name, 40), Status: c.Status(now)})
		}
		if p, err := h.axiCredentialPath(); err == nil {
			d.AxiPath = p
			if existing, err := axicred.Existing(p); err == nil && existing != "" {
				d.HasSavedCred = true
			}
		}
	}
	if h.service != nil && h.service.Where() != "" {
		d.ServiceAvailable, d.Service = true, h.service.Where()
		d.ServiceInstalled, _ = h.service.Installed()
	}
	h.setupRender(w, d)
}

func (h *handler) setupRender(w http.ResponseWriter, d *setupData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := setupTemplate.Execute(w, d); err != nil {
		http.Error(w, "page error", http.StatusInternalServerError)
	}
}

// setupChats saves which chats are captured: the ticked ones on, the other
// listed ones off. This is a capture choice, not agent access, so it needs no
// system prompt; an agent still sees nothing it has not been granted.
func (h *handler) setupChats(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		h.uiBack(w, r, "unpaired")
		return
	}
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	if len(groups) > maxSetupChats {
		groups = groups[:maxSetupChats]
	}
	want := map[string]bool{}
	for _, id := range r.PostForm["chat"] {
		want[id] = true
	}
	var on, off []string
	for _, g := range groups {
		switch {
		case want[g.ID] && !g.Enabled:
			on = append(on, g.ID)
		case !want[g.ID] && g.Enabled:
			off = append(off, g.ID)
		}
	}
	for _, change := range []struct {
		ids     []string
		enabled bool
	}{{on, true}, {off, false}} {
		if len(change.ids) == 0 {
			continue
		}
		if err := h.store.SetEnabled(r.Context(), account.ID, change.ids, change.enabled); err != nil {
			h.uiBack(w, r, "failed")
			return
		}
	}
	h.uiBack(w, r, "saved")
}

// setupAgent creates an agent credential and saves it where whatsapp-axi reads
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
	if !h.human.Enabled() {
		h.uiBack(w, r, "nogate")
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if agentauth.ValidName(name) != nil {
		h.uiBack(w, r, "badname")
		return
	}
	// Read only, until revoked. Sending stays behind the approval of each request.
	scopes := []string{agentauth.ScopeReadChats, agentauth.ScopeReadMessages}
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
	req := agentauth.GrantRequest{Name: name, Scopes: scopes, OnDemand: true, RatePerMinute: agentauth.DefaultRatePerMinute}
	if agentauth.ValidateGrant(req) != nil {
		h.uiBack(w, r, "invalid")
		return
	}
	if err := h.confirmError(r.Context(), agentauth.GrantEffect(name, scopes, nil, true, 0)); err != nil {
		h.uiBack(w, r, noticeFor(err))
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

// setupLink asks for a new pairing code after linking stopped.
func (h *handler) setupLink(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if !h.setup.Retry() {
		h.uiBack(w, r, "")
		return
	}
	h.uiBack(w, r, "newcode")
}

// setupService turns start-at-login on or off. Turning it on needs the system
// prompt because it leaves a program that runs whenever the person logs in.
func (h *handler) setupService(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) || h.service == nil {
		return
	}
	switch r.PostForm.Get("action") {
	case "install":
		if !h.human.Enabled() {
			h.uiBack(w, r, "nogate")
			return
		}
		if err := h.confirmError(r.Context(), "Approve: start wasync by itself every time you log in, and keep it running in the background"); err != nil {
			h.uiBack(w, r, noticeFor(err))
			return
		}
		if err := h.service.Install(h.serviceSpec); err != nil {
			h.uiBack(w, r, "failed")
			return
		}
		h.uiBack(w, r, "loginon")
	case "remove":
		if err := h.service.Uninstall(); err != nil {
			h.uiBack(w, r, "failed")
			return
		}
		h.uiBack(w, r, "loginoff")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

var setupTemplate = template.Must(template.New("setup").Parse(uiHead + `{{template "head" .}}
<h1>Set up wasync</h1>
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}

<h2>1. Link WhatsApp</h2>
<section class="card">
{{if .Paired}}<p class="ok"><strong>Linked.</strong> wasync is connected to your WhatsApp.</p>
{{else if .Pairing}}<p>On your phone open <strong>WhatsApp → Linked devices → Link a device</strong> and scan this code. It refreshes by itself.</p>
<div style="max-width:18rem;margin:.5rem 0">{{.QR}}</div>
{{else if .Failure}}<p class="bad">{{.Failure}}</p>
<form method="post" action="/ui/setup/link"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="go">Get a new code</button></form>
{{else}}<p class="mut">Waiting for WhatsApp to offer a code… If nothing appears, run <code>wasync setup</code> in a terminal.</p>{{end}}
</section>

{{if .Paired}}
<h2>2. Choose chats to keep</h2>
<section class="card">
<form method="post" action="/ui/setup/chats"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="mut">wasync archives only the chats you tick, on this computer. Agents see none of them until you connect one in step 3 and approve what it may read.</p>
<fieldset><legend>Chats</legend>
{{range .Chats}}<label><input type="checkbox" name="chat" value="{{.ID}}"{{if .Enabled}} checked{{end}}> {{.Name}} <span class="mut">{{.Kind}}</span></label>
{{else}}<p class="mut">No chats yet. They appear a moment after linking; use Refresh.</p>{{end}}
{{if .MoreChats}}<p class="mut">{{.MoreChats}} more chats are not listed here. Use the full-screen view (<code>wasync</code>) to pick them.</p>{{end}}</fieldset>
<button class="go">Save selection</button>
</form></section>

<h2>3. Connect an agent</h2>
<section class="card">
{{if .Agents}}<p>Connected agents:</p><ul>{{range .Agents}}<li>{{.Name}} <span class="mut">{{.Status}}</span></li>{{end}}</ul>{{end}}
{{if .Gate}}
<form method="post" action="/ui/setup/agent"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="mut">The agent may read only chats you approve, and starts with none. When it asks for one, the request appears under Approvals and you decide. The secret is saved straight to where whatsapp-axi reads it ({{.AxiPath}}) and is never shown.</p>
<label>Name <input name="name" value="whatsapp-axi" maxlength="32" pattern="[a-z0-9-]{1,32}"></label>
{{if .HasSavedCred}}<label><input type="checkbox" name="replace" value="yes"> Replace the credential whatsapp-axi already has saved</label>{{end}}
<button class="go">Connect…</button> <span class="mut">Asks you to confirm on this computer.</span>
</form>
{{else}}<p class="bad">This computer cannot show a system confirmation, so agents cannot be connected from this page. Run <code>wasync agents grant whatsapp-axi --on-demand</code> in a terminal.</p>{{end}}
</section>
{{end}}

{{if .ServiceAvailable}}
<h2>{{if .Paired}}4{{else}}2{{end}}. Start wasync when you log in</h2>
<section class="card">
<p class="mut">Keeps capture and approvals working without opening a terminal. It is {{.Service}} for your account only and needs no administrator rights. It takes effect the next time you log in.</p>
<form method="post" action="/ui/setup/service"><input type="hidden" name="csrf" value="{{.CSRF}}">
{{if .ServiceInstalled}}<p class="ok"><strong>On.</strong></p><button name="action" value="remove">Turn off</button>
{{else}}{{if .Gate}}<button class="go" name="action" value="install">Turn on…</button> <span class="mut">Asks you to confirm on this computer.</span>
{{else}}<p class="bad">This computer cannot show a system confirmation, so run <code>wasync service install</code> in a terminal.</p>{{end}}{{end}}
</form></section>
{{end}}
<p><a href="/ui/setup">Refresh</a></p>
</main></body></html>
`))
