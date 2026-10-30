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
	d.Paired = account != nil || snap.Paired
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
	withHistory := r.PostForm.Get("history") == "full"
	want := map[string]bool{}
	switch r.PostForm.Get("action") {
	case "all-groups":
		for _, g := range groups {
			want[g.ID] = storage.ChatKind(g.ID) == "group"
		}
	case "none":
	case "save":
		for _, id := range r.PostForm["chat"] {
			want[id] = true
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	var off []string
	for _, g := range groups {
		switch {
		case want[g.ID]:
			if err := h.store.EnableCapture(r.Context(), account.ID, g.ID, withHistory); err != nil {
				h.uiBack(w, r, "failed")
				return
			}
		case g.Enabled:
			off = append(off, g.ID)
		}
	}
	if len(off) > 0 {
		if err := h.store.SetEnabled(r.Context(), account.ID, off, false); err != nil {
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
	scopes := []string{agentauth.ScopeReadChats, agentauth.ScopeReadMessages}
	if r.PostForm.Get("access") == "read-send" {
		scopes = append(scopes, agentauth.ScopeSend)
	}
	var expires time.Duration
	switch r.PostForm.Get("expires") {
	case "30d":
		expires = 720 * time.Hour
	case "90d":
		expires = 2160 * time.Hour
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
	req := agentauth.GrantRequest{Name: name, Scopes: scopes, OnDemand: true, RatePerMinute: agentauth.DefaultRatePerMinute, Expires: expires}
	if agentauth.ValidateGrant(req) != nil {
		h.uiBack(w, r, "invalid")
		return
	}
	if err := h.confirmError(r.Context(), agentauth.GrantEffect(name, scopes, nil, true, expires)); err != nil {
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
{{else if .Failure}}<p class="bad">{{.Failure}}</p><p class="mut">Run <code>wasync setup</code> again to get a new code.</p>
{{else}}<p class="mut">Waiting for WhatsApp to offer a code… If nothing appears, run <code>wasync setup</code> in a terminal.</p>{{end}}
</section>

{{if .Paired}}
<h2>2. Choose chats to keep</h2>
<section class="card">
<form method="post" action="/ui/setup/chats"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="mut">wasync archives only the chats you tick, on this computer. Agents see none of them until you connect one in step 3 and approve what it may read.</p>
<fieldset><legend>Older messages</legend>
<label><input type="radio" name="history" value="now" checked> From now on only</label>
<label><input type="radio" name="history" value="full"> Also ask my phone for older messages (best effort)</label></fieldset>
<fieldset><legend>Chats</legend>
{{range .Chats}}<label><input type="checkbox" name="chat" value="{{.ID}}"{{if .Enabled}} checked{{end}}> {{.Name}} <span class="mut">{{.Kind}}</span></label>
{{else}}<p class="mut">No chats yet. They appear a moment after linking; use Refresh.</p>{{end}}
{{if .MoreChats}}<p class="mut">{{.MoreChats}} more chats are not listed here. Use the full-screen view (<code>wasync</code>) to pick them.</p>{{end}}</fieldset>
<button class="go" name="action" value="save">Save selection</button>
<button name="action" value="all-groups">Keep all groups</button>
<button name="action" value="none">Keep none</button>
</form></section>

<h2>3. Connect an agent</h2>
<section class="card">
{{if .Agents}}<p>Connected agents:</p><ul>{{range .Agents}}<li>{{.Name}} <span class="mut">{{.Status}}</span></li>{{end}}</ul>{{end}}
{{if .Gate}}
<form method="post" action="/ui/setup/agent"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="mut">The agent starts with no chats. When it asks for one, the request appears under Approvals and you decide. The secret is saved straight to where whatsapp-axi reads it ({{.AxiPath}}) and is never shown.</p>
<label>Name <input name="name" value="whatsapp-axi" maxlength="32" pattern="[a-z0-9-]{1,32}"></label>
<fieldset><legend>What it may ask for</legend>
<label><input type="radio" name="access" value="read" checked> Read chats and messages (recommended)</label>
<label><input type="radio" name="access" value="read-send"> Read, and also send, reply and react in chats you approve</label></fieldset>
<fieldset><legend>Lasts</legend>
<label><input type="radio" name="expires" value="" checked> Until I revoke it</label>
<label><input type="radio" name="expires" value="90d"> 90 days</label>
<label><input type="radio" name="expires" value="30d"> 30 days</label></fieldset>
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
