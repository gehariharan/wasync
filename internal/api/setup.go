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
)

// The setup page walks a first-time person through what the terminal used to:
// link WhatsApp by QR code, choose chats, connect an agent, and start wasync
// at login. Like the approval page it has no script and is not a security
// boundary. Choices that widen what an agent can do or start a background
// program at login still end in the operating system's confirmation prompt (or
// a passkey, from another device on the tailnet), and where neither is
// available the page only shows the command to run instead. With the tailnet
// opt-in on it also lists the devices set up to approve with a passkey; only
// this computer can add one.

type setupAgent struct{ Name, Status string }

type setupData struct {
	uiData
	Paired, Pairing   bool
	QR                template.HTML
	Failure           string
	Syncing, Total    int
	Agents            []setupAgent
	AxiPath           string
	HasSavedCred      bool
	Service           string
	ServiceInstalled  bool
	ServiceAvailable  bool
	ServiceNeedsHuman bool
	// Tailnet is on when the web app opens from other devices on the tailnet.
	Tailnet      bool
	TailnetHost  string
	TailnetLogin string
	Devices      []deviceRow
	// CanAddDevice is set on this computer when it can show the system prompt.
	CanAddDevice bool
	// DeviceCode is a code just made for setting up a device.
	DeviceCode string
}

func (h *handler) axiCredentialPath() (string, error) {
	if h.axiPath != nil {
		return h.axiPath()
	}
	return axicred.Path(os.Getenv)
}

func (h *handler) setupHome(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.setupView(w, r, sess, nil)
}

// setupView renders the setup page; tweak, when set, adds to what it shows.
func (h *handler) setupView(w http.ResponseWriter, r *http.Request, sess uiSession, tweak func(*setupData)) {
	d := &setupData{uiData: uiData{CSRF: sess.csrf, Gate: h.confirmName(r), Title: "wasync setup", Remote: remoteLogin(r) != ""}}
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
		d.Total = len(groups)
		for _, g := range groups {
			if g.Enabled {
				d.Syncing++
			}
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
	if s, devices := h.remoteDevices(); s.Host != "" {
		d.Tailnet, d.TailnetHost, d.TailnetLogin = true, s.Host, s.Login
		d.Devices = deviceRows(devices)
		d.CanAddDevice = !d.Remote && h.human.Enabled()
	}
	if tweak != nil {
		tweak(d)
	}
	h.setupRender(w, d)
}

func (h *handler) setupRender(w http.ResponseWriter, d *setupData) {
	h.warnBackground(d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := setupTemplate.Execute(w, d); err != nil {
		http.Error(w, "page error", http.StatusInternalServerError)
	}
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
		if !h.canConfirm(r) {
			h.uiBack(w, r, noGate(r))
			return
		}
		if err := h.confirmError(r.Context(), "Approve: start wasync by itself every time you log in, and keep it running in the background"); err != nil {
			h.uiRefused(w, r, err, func(n string) { h.uiBack(w, r, n) })
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
<h2>2. Choose chats to sync</h2>
<section class="card">
{{if .Syncing}}<p><strong>{{.Syncing}} of {{.Total}} chats are syncing.</strong> They may be left over from before you linked this phone. <a href="/ui/chats/pick">Review them</a>, or start fresh.</p>
<form method="post" action="/ui/chats/pick"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="action" value="clearall">Start fresh: stop syncing all</button> <a href="/ui/chats/pick">Choose chats…</a></form>
{{else}}<p>Nothing is syncing yet. wasync keeps only the chats you tick, on this computer. Agents see none of them until you connect one in step 3 and approve what it may read.</p>
<p><a href="/ui/chats/pick"><strong>Choose chats…</strong></a> {{if .Total}}<span class="mut">{{.Total}} chats found.</span>{{else}}<span class="mut">Chats appear a moment after linking; refresh if none are listed.</span>{{end}}</p>{{end}}
</section>

<h2>3. Connect an agent</h2>
<section class="card">
{{if .Agents}}<p>Connected agents:</p><ul>{{range .Agents}}<li>{{.Name}} <span class="mut">{{.Status}}</span></li>{{end}}</ul>{{end}}
{{if .Gate}}
<form method="post" action="/ui/setup/agent"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="mut">The agent may read only chats you approve, and starts with none. When it asks for one, the request appears under Approvals and you decide. The secret is saved straight to where wasync-axi reads it ({{.AxiPath}}) and is never shown.</p>
<label>Name <input name="name" value="wasync-axi" maxlength="32" pattern="[a-z0-9-]{1,32}"></label>
{{if .HasSavedCred}}<label><input type="checkbox" name="replace" value="yes"> Replace the credential wasync-axi already has saved</label>{{end}}
<button class="go">Connect…</button> <span class="mut">Asks you to confirm with {{.Gate}}.</span>
</form>
{{else if .Remote}}<p class="bad">Connect agents on the computer running wasync, or <a href="#devices">set up this device</a> with a passkey first.</p>
{{else}}<p class="bad">This computer cannot show a system confirmation, so agents cannot be connected from this page. Run <code>wasync agents grant wasync-axi --on-demand</code> in a terminal.</p>{{end}}
</section>
{{end}}

{{if .ServiceAvailable}}
<h2 id="login">{{if .Paired}}4{{else}}2{{end}}. Start wasync when you log in</h2>
<section class="card">
<p class="mut">Keeps wasync running without a terminal. It is {{.Service}} for your account only.</p>
<form method="post" action="/ui/setup/service"><input type="hidden" name="csrf" value="{{.CSRF}}">
{{if .ServiceInstalled}}<p class="ok"><strong>On.</strong>{{if .Warn}} <span class="mut">To move this copy to the background, run <code>wasync service restart</code>.</span>{{end}}</p><button name="action" value="remove">Turn off</button>
{{else}}{{if .Gate}}<button class="go" name="action" value="install">Turn on…</button> <span class="mut">Asks you to confirm with {{.Gate}}.</span>
{{else if .Remote}}<p class="bad">Turn this on from the computer running wasync, or <a href="#devices">set up this device</a> with a passkey first.</p>
{{else}}<p class="bad">This computer cannot show a system confirmation, so run <code>wasync service install</code> in a terminal.</p>{{end}}{{end}}
</form></section>
{{end}}

{{if .Tailnet}}
<h2 id="devices">{{if .Remote}}Approve from this device{{else}}Approve from your other devices{{end}}</h2>
<section class="card">
<p class="mut">wasync opens at <code>https://{{.TailnetHost}}/ui/</code> for {{.TailnetLogin}} on your tailnet. A device set up here can approve agents' requests and give agents more power: each time, it shows what you are approving and asks for its passkey (Touch ID, Face ID, Windows Hello or the device's PIN). Only the computer running wasync can add a device.</p>
{{if .Devices}}<table class="sent"><thead><tr><th>Device</th><th>Added</th><th>Last approved</th><th></th></tr></thead><tbody>
{{range .Devices}}<tr><td>{{.Name}}</td><td>{{.Added}}</td><td>{{.Used}}</td><td><form method="post" action="/ui/setup/devices" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="remove"><input type="hidden" name="id" value="{{.ID}}"><button>Remove</button></form></td></tr>
{{end}}</tbody></table>{{else}}<p>No device is set up yet.</p>{{end}}
{{if .Remote}}<form method="post" action="/ui/passkey/begin"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p><label>Code from the computer running wasync <input name="code" autocomplete="one-time-code" autocapitalize="characters" spellcheck="false" size="16" maxlength="20" required></label></p>
<p><label>Name this device <input name="name" maxlength="40" placeholder="My laptop"></label></p>
<p><button class="go">Set up this device…</button> <span class="mut">Get a code there: open the web app, Set up, Add a device. A device that already has a passkey for wasync (one synced from another device too) needs no setting up.</span></p></form>
{{else if .DeviceCode}}<p class="ok" role="status">On the other device, open <code>https://{{.TailnetHost}}/ui/setup</code> and enter this code within 10 minutes. It works once.</p>
<p style="font-size:1.6rem;letter-spacing:.12em"><strong>{{.DeviceCode}}</strong></p>
{{else if .CanAddDevice}}<form method="post" action="/ui/setup/devices#devices"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="add"><button class="go">Add a device…</button> <span class="mut">Asks you to confirm with {{.Gate}}, then shows a one-time code to enter on the other device.</span></form>
{{else}}<p class="bad">This computer cannot show a system confirmation, so devices cannot be added.</p>{{end}}
</section>
{{end}}
<p><a href="/ui/setup">Refresh</a></p>
</main></body></html>
`))
